// Package rpc implements hq's daemon protocol: newline-delimited JSON over a
// unix socket. Two frame kinds flow daemon→client: responses (matched to a
// request id) and events (pushed to subscribers). Clients send requests.
package rpc

import (
	"encoding/json"
)

type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Event is pushed to clients that called events.subscribe.
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// Event names.
const (
	EvMessageNew     = "message.new"     // data: store.Message
	EvTicketUpdated  = "ticket.updated"  // data: store.Ticket
	EvProjectCreated = "project.created" // data: store.Project
	EvItemNew        = "item.new"        // data: store.Item — something needs the boss
	EvItemResolved   = "item.resolved"   // data: store.Item
	EvAgentStream    = "agent.stream"    // data: StreamUpdate — ephemeral, never persisted
)

// StreamUpdate is a live in-progress view of an agent's current turn: the
// accumulating assistant text as it streams, or a one-line tool-activity
// note. Ephemeral — the TUI paints it under the thread and drops it when
// the real message row lands (or Body is empty).
type StreamUpdate struct {
	ProjectID string `json:"project_id"`
	TicketID  string `json:"ticket_id"` // "" = the project/director chat
	Author    string `json:"author"`    // em | eng:<ticket>
	Body      string `json:"body"`      // text so far; "" clears the bubble
}
