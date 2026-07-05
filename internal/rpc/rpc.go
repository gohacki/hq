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
)
