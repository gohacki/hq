// Package rpc implements shipyard's daemon protocol: newline-delimited JSON
// over a unix socket. Two frame kinds flow daemon→client: responses (matched
// to a request id) and events (pushed to subscribers). Clients send requests.
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
	Event string          `json:"event"` // e.g. message.new, task.updated, channel.created
	Data  json.RawMessage `json:"data"`
}

// frame is the wire envelope daemon→client; exactly one field set.
type frame struct {
	*Response
	*Event
}

// Event names.
const (
	EvMessageNew     = "message.new"     // data: store.Message
	EvTaskUpdated    = "task.updated"    // data: store.Task
	EvChannelCreated = "channel.created" // data: store.Channel
	EvNeedsInput     = "needs.input"     // data: NeedsInput
)

// NeedsInput signals that a thread requires the captain's attention.
type NeedsInput struct {
	ChannelID string `json:"channel_id"`
	TaskID    string `json:"task_id,omitempty"`
	Reason    string `json:"reason"` // gate | question | blocked | failed
	Summary   string `json:"summary"`
}
