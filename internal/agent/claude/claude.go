// Package claude runs Claude Code headless over stream-json. One process per
// session; user messages stream in on stdin, events stream out on stdout.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/gohacki/shipyard/internal/agent"
)

type Harness struct {
	// Bin overrides the claude binary path (tests use a fake).
	Bin string
	// Model is the default model for all sessions ("" = claude's default).
	// SHIPYARD_MODEL overrides it at daemon start (e.g. "sonnet" to run the
	// whole fleet on a cheaper tier).
	Model string
}

func New() *Harness { return &Harness{Bin: "claude", Model: os.Getenv("SHIPYARD_MODEL")} }

func (h *Harness) InteractiveCommand(sessionID string) []string {
	return []string{h.Bin, "--resume", sessionID}
}

func (h *Harness) Start(ctx context.Context, spec agent.Spec) (agent.Session, error) {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose", // required with -p + stream-json output
	}
	model := spec.Model
	if model == "" {
		model = h.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if spec.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", spec.SystemPrompt)
	}
	if spec.ResumeSessionID != "" {
		args = append(args, "--resume", spec.ResumeSessionID)
	}
	if spec.MCPConfigPath != "" {
		args = append(args, "--mcp-config", spec.MCPConfigPath)
	}
	for _, t := range spec.AllowedTools {
		args = append(args, "--allowedTools", t)
	}
	if spec.Autonomous {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		args = append(args, "--permission-mode", "acceptEdits")
	}

	cmd := exec.CommandContext(ctx, h.Bin, args...)
	cmd.Dir = spec.WorkDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &session{
		cmd:    cmd,
		stdin:  stdin,
		events: make(chan agent.Event, 256),
	}
	go s.readLoop(stdout)
	if spec.Prompt != "" {
		if err := s.Send(spec.Prompt); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

type session struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan agent.Event

	mu        sync.Mutex
	sessionID string
}

func (s *session) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

func (s *session) Send(text string) error {
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

func (s *session) Events() <-chan agent.Event { return s.events }

func (s *session) Interrupt() error {
	// stream-json control request; Claude Code stops the current turn.
	b, _ := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": fmt.Sprintf("int-%d", time.Now().UnixNano()),
		"request":    map[string]any{"subtype": "interrupt"},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stdin.Write(append(b, '\n'))
	return err
}

func (s *session) Close() error {
	s.stdin.Close()
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	return nil
}

// stream-json output frames we care about.
type outFrame struct {
	Type      string `json:"type"`    // system | assistant | user | result
	Subtype   string `json:"subtype"` // init (system), success/error... (result)
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Message   struct {
		Content []struct {
			Type string `json:"type"` // text | tool_use
			Text string `json:"text"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
}

func (s *session) readLoop(stdout io.Reader) {
	defer close(s.events)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
	for sc.Scan() {
		var f outFrame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			continue
		}
		switch f.Type {
		case "system":
			if f.Subtype == "init" && f.SessionID != "" {
				s.mu.Lock()
				s.sessionID = f.SessionID
				s.mu.Unlock()
				s.events <- agent.Event{Kind: agent.EvInit, SessionID: f.SessionID}
			}
		case "assistant":
			for _, c := range f.Message.Content {
				switch c.Type {
				case "text":
					if c.Text != "" {
						s.events <- agent.Event{Kind: agent.EvText, Text: c.Text}
					}
				case "tool_use":
					s.events <- agent.Event{Kind: agent.EvToolUse, Tool: c.Name}
				}
			}
		case "result":
			s.events <- agent.Event{Kind: agent.EvResult, Text: f.Result, IsError: f.IsError, SessionID: f.SessionID}
		}
	}
	err := s.cmd.Wait()
	s.events <- agent.Event{Kind: agent.EvExited, Err: err}
}
