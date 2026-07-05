// Package agent defines the harness abstraction shipyard uses to run coding
// agents. Claude Code is the v1 implementation; other harnesses plug in
// behind the same interface.
package agent

import "context"

// Spec describes one agent session to start.
type Spec struct {
	Model           string   // model override ("" = harness default)
	WorkDir         string   // cwd for the agent process
	SystemPrompt    string   // appended system prompt (role, tools contract)
	Prompt          string   // initial user prompt (brief); optional when resuming
	ResumeSessionID string   // resume an existing session ("" = fresh)
	MCPConfigPath   string   // path to MCP servers JSON ("" = none)
	AllowedTools    []string // extra allowed tools (MCP tool names)
	Autonomous      bool     // full permissions (crewmates in isolated worktrees)
}

// EventKind classifies what an agent session emitted.
type EventKind string

const (
	EvInit    EventKind = "init"     // session started; SessionID set
	EvText    EventKind = "text"     // assistant text block
	EvToolUse EventKind = "tool_use" // assistant used a tool (name in Tool)
	EvResult  EventKind = "result"   // turn finished; Text = final result
	EvExited  EventKind = "exited"   // process ended; Err set on failure
)

type Event struct {
	Kind      EventKind
	SessionID string
	Text      string
	Tool      string
	IsError   bool
	Err       error
}

// Session is one live agent process.
type Session interface {
	// SessionID is available after the init event.
	SessionID() string
	// Send delivers a user message to the running session.
	Send(text string) error
	// Events streams session output; closed when the process exits.
	Events() <-chan Event
	// Interrupt asks the agent to stop its current turn.
	Interrupt() error
	// Close terminates the process.
	Close() error
}

// Harness starts agent sessions.
type Harness interface {
	Start(ctx context.Context, spec Spec) (Session, error)
	// InteractiveCommand returns argv to resume a session interactively in a
	// terminal (the tmux escape hatch), on the given model ("" = default),
	// with any extra harness args (e.g. an MCP config).
	InteractiveCommand(sessionID, model string, extraArgs ...string) []string
}
