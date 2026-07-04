package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeToolsFlow(t *testing.T) {
	srv := &Server{Name: "test", Version: "0", Tools: []Tool{{
		Name:        "greet",
		Description: "greets",
		InputSchema: map[string]any{"type": "object"},
		Run: func(args json.RawMessage) (string, error) {
			var p struct{ Name string }
			json.Unmarshal(args, &p)
			return "hello " + p.Name, nil
		},
	}}}

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"greet","arguments":{"Name":"cap"}}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := srv.Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 responses (notification skipped), got %d: %s", len(lines), out.String())
	}
	if !strings.Contains(lines[1], `"greet"`) {
		t.Fatalf("tools/list missing tool: %s", lines[1])
	}
	if !strings.Contains(lines[2], "hello cap") {
		t.Fatalf("tools/call result wrong: %s", lines[2])
	}
}
