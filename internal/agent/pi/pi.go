// Package pi drives the pi coding agent (github.com/earendil-works/pi) as
// hq's open-source harness for the director and EMs. pi runs in RPC mode —
// a plain subprocess speaking JSONL commands and events over stdio — so the
// TUI renders its conversation natively; no PTY, no terminal embedding.
//
// Session identity is the pi session *file path* (stable across restarts);
// hq stores it wherever it stores claude session ids and resumes with
// `--session <path>`. pi has no MCP: EM tools reach the daemon through the
// `hq em` CLI, documented in the role prompt (see orch.cliToolsSection).
package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/gohacki/hq/internal/agent"
)

type Harness struct {
	Bin        string
	Model      string // default model alias when a spec has none
	SessionDir string // where pi session files live
}

func New(sessionDir string) *Harness {
	model := os.Getenv("HQ_MODEL")
	if model == "" {
		model = "sonnet"
	}
	return &Harness{Bin: "pi", Model: model, SessionDir: sessionDir}
}

// ResolveModel maps hq's model aliases onto concrete Anthropic model ids —
// pi resolves models against its own catalog, not claude's alias names.
func ResolveModel(m string) string {
	switch m {
	case "", "sonnet":
		return "claude-sonnet-5"
	case "opus":
		return "claude-opus-4-8"
	case "haiku":
		return "claude-haiku-4-5"
	case "fable":
		return "claude-fable-5"
	}
	return m
}

func (h *Harness) SupportsMCP() bool { return false }

// InteractiveCommand opens pi's own TUI on the same session file — used by
// desk visits (`v` attach).
func (h *Harness) InteractiveCommand(sessionID, model string, extraArgs ...string) []string {
	cmd := []string{h.Bin, "--provider", "anthropic"}
	if sessionID != "" {
		cmd = append(cmd, "--session", sessionID)
	}
	if model == "" {
		model = h.Model
	}
	cmd = append(cmd, "--model", ResolveModel(model))
	return append(cmd, extraArgs...)
}

func (h *Harness) Start(ctx context.Context, spec agent.Spec) (agent.Session, error) {
	if err := os.MkdirAll(h.SessionDir, 0o755); err != nil {
		return nil, err
	}
	args := []string{
		"--mode", "rpc",
		"--provider", "anthropic",
		"--session-dir", h.SessionDir,
		"--no-approve", // never trust project-local extensions from RPC: no one is at the dialog
	}
	model := spec.Model
	if model == "" {
		model = h.Model
	}
	args = append(args, "--model", ResolveModel(model))
	if spec.SystemPrompt != "" {
		// Append, keeping pi's small default prompt underneath the role —
		// same shape as claude's --append-system-prompt.
		args = append(args, "--append-system-prompt", spec.SystemPrompt)
	}
	if spec.ResumeSessionID != "" {
		// Our pi session ids are file paths. Anything else (a claude UUID
		// left over from a harness switch, a deleted file) would make pi
		// print "No session found" and exit 0 — silently. Start fresh
		// instead; the new id is persisted on init as usual.
		if _, err := os.Stat(spec.ResumeSessionID); err == nil {
			args = append(args, "--session", spec.ResumeSessionID)
		}
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
	stderrTail := &tailBuffer{}
	cmd.Stderr = stderrTail
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &session{
		cmd:    cmd,
		stdin:  stdin,
		stderr: stderrTail,
		events: make(chan agent.Event, 256),
	}
	go s.readLoop(stdout)
	// The session file is this session's durable identity; ask for it up
	// front so the caller can persist it (same role as claude's init frame).
	if err := s.write(map[string]any{"id": "hq-init", "type": "get_state"}); err != nil {
		s.Close()
		return nil, err
	}
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
	stderr *tailBuffer
	events chan agent.Event

	mu        sync.Mutex
	sessionID string
	lastText  string // last assistant message text; agent_end's EvResult carries it
	lastErr   string // provider error from an errored assistant message, if any
	closed    bool   // Close() called — an exit afterwards is expected, not an error
}

// tailBuffer keeps the last chunk of pi's stderr so a startup death has a
// story to tell.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2048 {
		t.buf = t.buf[len(t.buf)-2048:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

func (s *session) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

func (s *session) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

// Send delivers a user message. streamingBehavior steer makes a mid-turn
// message queue as steering instead of erroring — matching how hq's chat
// composer expects "just say it, the agent picks it up".
func (s *session) Send(text string) error {
	return s.write(map[string]any{"type": "prompt", "message": text, "streamingBehavior": "steer"})
}

func (s *session) Events() <-chan agent.Event { return s.events }

func (s *session) Interrupt() error {
	return s.write(map[string]any{"type": "abort"})
}

func (s *session) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stdin.Close() // pi exits when stdin closes
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	return nil
}

// --- pi RPC frames we care about ---

type frame struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Command string `json:"command"`
	Success *bool  `json:"success"`
	Error   string `json:"error"`
	Data    struct {
		SessionFile string `json:"sessionFile"`
		SessionID   string `json:"sessionId"`
	} `json:"data"`
	Message    *piMessage `json:"message"`
	FinalError string     `json:"finalError"`
	// extension UI dialogs need an answer or the extension hangs
	Method string `json:"method"`
}

type piMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	// An errored turn arrives as an assistant message with empty content,
	// stopReason "error", and the provider error here (e.g. 400 no extra
	// usage) — it must surface, not vanish.
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
}

func (m *piMessage) text() string {
	if m == nil {
		return ""
	}
	out := ""
	for _, c := range m.Content {
		if c.Type == "text" {
			if out != "" {
				out += "\n"
			}
			out += c.Text
		}
	}
	return out
}

func (s *session) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var f frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			continue
		}
		switch f.Type {
		case "response":
			if f.ID == "hq-init" && f.Command == "get_state" {
				id := f.Data.SessionFile
				if id == "" {
					id = f.Data.SessionID
				}
				s.mu.Lock()
				s.sessionID = id
				s.mu.Unlock()
				s.events <- agent.Event{Kind: agent.EvInit, SessionID: id}
			} else if f.Success != nil && !*f.Success {
				s.events <- agent.Event{Kind: agent.EvResult, IsError: true, Text: f.Command + ": " + f.Error}
			}
		case "message_end":
			if f.Message != nil && f.Message.Role == "assistant" {
				if f.Message.StopReason == "error" || f.Message.ErrorMessage != "" {
					s.mu.Lock()
					s.lastErr = f.Message.ErrorMessage
					if s.lastErr == "" {
						s.lastErr = "provider error (no detail)"
					}
					s.mu.Unlock()
				} else if txt := f.Message.text(); txt != "" {
					s.mu.Lock()
					s.lastText = txt
					s.mu.Unlock()
					s.events <- agent.Event{Kind: agent.EvText, Text: txt}
				}
			}
		case "agent_end":
			s.mu.Lock()
			txt, errText := s.lastText, s.lastErr
			s.lastText, s.lastErr = "", ""
			s.mu.Unlock()
			if errText != "" {
				s.events <- agent.Event{Kind: agent.EvResult, IsError: true, Text: errText}
			} else {
				s.events <- agent.Event{Kind: agent.EvResult, Text: txt}
			}
		case "auto_retry_end":
			if f.Success != nil && !*f.Success {
				s.events <- agent.Event{Kind: agent.EvResult, IsError: true, Text: f.FinalError}
			}
		case "extension_ui_request":
			// No human is on this channel; cancel dialogs so nothing hangs.
			switch f.Method {
			case "confirm":
				s.write(map[string]any{"type": "extension_ui_response", "id": f.ID, "confirmed": false})
			case "select", "input", "editor":
				s.write(map[string]any{"type": "extension_ui_response", "id": f.ID, "cancelled": true})
			}
		}
	}
	s.cmd.Wait()
	s.mu.Lock()
	diedEarly := s.sessionID == "" && !s.closed
	s.mu.Unlock()
	if diedEarly {
		// pi exited before completing init (bad flag, unknown session,
		// auth problem) — without this the chat just goes quiet.
		msg := "pi exited at startup"
		if tail := s.stderr.String(); tail != "" {
			msg += ": " + tail
		}
		s.events <- agent.Event{Kind: agent.EvResult, IsError: true, Text: msg}
	}
	s.events <- agent.Event{Kind: agent.EvExited}
	close(s.events)
}
