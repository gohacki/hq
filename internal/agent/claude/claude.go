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
	"strings"
	"sync"
	"time"

	"github.com/gohacki/hq/internal/agent"
)

type Harness struct {
	// Bin overrides the claude binary path (tests use a fake).
	Bin string
	// Model is the department-wide default. hq defaults to sonnet — cheap
	// and fast; upgrade specific agents on the fly with /model or by asking
	// the EM. HQ_MODEL overrides the default.
	Model string
}

// DefaultModel is what every agent runs on unless told otherwise.
const DefaultModel = "sonnet"

func New() *Harness {
	model := os.Getenv("HQ_MODEL")
	if model == "" {
		model = DefaultModel
	}
	return &Harness{Bin: "claude", Model: model}
}

// ResolveModel maps friendly names to what the claude CLI accepts; sonnet,
// opus, and haiku are native aliases.
func ResolveModel(m string) string {
	switch m {
	case "fable":
		return "claude-fable-5"
	default:
		return m
	}
}

func (h *Harness) SupportsMCP() bool { return true }

func (h *Harness) InteractiveCommand(sessionID, model string, extraArgs ...string) []string {
	if model == "" {
		model = h.Model
	}
	argv := []string{h.Bin, "--resume", sessionID}
	if model != "" {
		argv = append(argv, "--model", ResolveModel(model))
	}
	return append(argv, extraArgs...)
}

func (h *Harness) Start(ctx context.Context, spec agent.Spec) (agent.Session, error) {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",                  // required with -p + stream-json output
		"--include-partial-messages", // stream_event frames feed the live typing bubble
	}
	model := spec.Model
	if model == "" {
		model = h.Model
	}
	if model != "" {
		args = append(args, "--model", ResolveModel(model))
	}
	if spec.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", spec.SystemPrompt)
	}
	if spec.ResumeSessionID != "" && !strings.Contains(spec.ResumeSessionID, "/") {
		// Claude session ids are UUIDs; a path here is another harness's
		// session (e.g. pi's file) left over from a harness switch — start
		// fresh instead of resuming something claude can't load.
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
	partial   string // accumulating text of the currently streaming block
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
	Type      string `json:"type"`    // system | assistant | user | result | stream_event
	Subtype   string `json:"subtype"` // init (system), success/error... (result)
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Message   struct {
		Content []struct {
			Type  string          `json:"type"` // text | tool_use
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	// stream_event payload (--include-partial-messages): the raw Anthropic
	// SSE event; only text deltas matter here.
	Event struct {
		Type  string `json:"type"` // content_block_delta | content_block_stop | ...
		Delta struct {
			Type string `json:"type"` // text_delta | thinking_delta | input_json_delta
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
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
		case "stream_event":
			// Live typing: accumulate text deltas and re-emit text-so-far.
			// Non-blocking — the full assistant frame is authoritative, so a
			// slow consumer just skips deltas rather than stalling stdout.
			if f.Event.Type == "content_block_delta" && f.Event.Delta.Type == "text_delta" && f.Event.Delta.Text != "" {
				s.mu.Lock()
				s.partial += f.Event.Delta.Text
				partial := s.partial
				s.mu.Unlock()
				select {
				case s.events <- agent.Event{Kind: agent.EvTextDelta, Text: partial}:
				default:
				}
			}
		case "assistant":
			s.mu.Lock()
			s.partial = ""
			s.mu.Unlock()
			for _, c := range f.Message.Content {
				switch c.Type {
				case "text":
					if c.Text != "" {
						s.events <- agent.Event{Kind: agent.EvText, Text: c.Text}
					}
				case "tool_use":
					s.events <- agent.Event{Kind: agent.EvToolUse, Tool: c.Name, Text: agent.SummarizeArgs(c.Input)}
				}
			}
		case "result":
			s.events <- agent.Event{Kind: agent.EvResult, Text: f.Result, IsError: f.IsError, SessionID: f.SessionID}
		}
	}
	err := s.cmd.Wait()
	s.events <- agent.Event{Kind: agent.EvExited, Err: err}
}
