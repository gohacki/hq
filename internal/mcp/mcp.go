// Package mcp is a minimal Model Context Protocol stdio server — just enough
// for Claude Code to call shipyard's lead tools (initialize, tools/list,
// tools/call over JSON-RPC 2.0, newline-delimited).
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

type Tool struct {
	Name        string                                     `json:"name"`
	Description string                                     `json:"description"`
	InputSchema map[string]any                             `json:"inputSchema"`
	Run         func(args json.RawMessage) (string, error) `json:"-"`
}

type Server struct {
	Name    string
	Version string
	Tools   []Tool
}

type req struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type resp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *respError      `json:"error,omitempty"`
}

type respError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads JSON-RPC from r and writes responses to w until EOF.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var rq req
		if err := json.Unmarshal(sc.Bytes(), &rq); err != nil {
			continue
		}
		if rq.ID == nil { // notification (e.g. notifications/initialized)
			continue
		}
		out := s.dispatch(rq)
		if err := enc.Encode(out); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) dispatch(rq req) resp {
	switch rq.Method {
	case "initialize":
		return resp{JSONRPC: "2.0", ID: rq.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}}
	case "tools/list":
		return resp{JSONRPC: "2.0", ID: rq.ID, Result: map[string]any{"tools": s.Tools}}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(rq.Params, &p); err != nil {
			return resp{JSONRPC: "2.0", ID: rq.ID, Error: &respError{Code: -32602, Message: err.Error()}}
		}
		for _, t := range s.Tools {
			if t.Name == p.Name {
				text, err := t.Run(p.Arguments)
				isErr := false
				if err != nil {
					text, isErr = err.Error(), true
				}
				return resp{JSONRPC: "2.0", ID: rq.ID, Result: map[string]any{
					"content": []map[string]any{{"type": "text", "text": text}},
					"isError": isErr,
				}}
			}
		}
		return resp{JSONRPC: "2.0", ID: rq.ID, Error: &respError{Code: -32601, Message: fmt.Sprintf("unknown tool %s", p.Name)}}
	case "ping":
		return resp{JSONRPC: "2.0", ID: rq.ID, Result: map[string]any{}}
	default:
		return resp{JSONRPC: "2.0", ID: rq.ID, Error: &respError{Code: -32601, Message: "method not supported: " + rq.Method}}
	}
}
