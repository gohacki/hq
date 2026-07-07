package pi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gohacki/hq/internal/agent"
)

// stubPi is a shell script speaking just enough of pi's RPC protocol to
// verify the adapter's event translation.
const stubPi = `#!/bin/sh
echo '{"id":"hq-init","type":"response","command":"get_state","success":true,"data":{"sessionFile":"/tmp/sess-1.jsonl"}}'
echo '{"type":"message_start","message":{"role":"assistant","content":[]}}'
echo '{"type":"message_update","message":{},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"hel"}}'
echo '{"type":"message_update","message":{},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"lo"}}'
echo '{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"ls -la"}}'
echo '{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{},"isError":false}'
echo '{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","text":"mull"},{"type":"text","text":"hello"},{"type":"text","text":"world"}]}}'
echo '{"type":"agent_end","messages":[]}'
echo '{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"400 no extra usage"}}'
echo '{"type":"agent_end","messages":[]}'
echo '{"type":"response","command":"prompt","success":false,"error":"boom"}'
cat >/dev/null
`

func TestEventTranslation(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "pi-stub")
	if err := os.WriteFile(bin, []byte(stubPi), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &Harness{Bin: bin, Model: "sonnet", SessionDir: filepath.Join(dir, "sessions")}
	sess, err := h.Start(context.Background(), agent.Spec{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	next := func() agent.Event {
		select {
		case ev := <-sess.Events():
			return ev
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for event")
			return agent.Event{}
		}
	}

	if ev := next(); ev.Kind != agent.EvInit || ev.SessionID != "/tmp/sess-1.jsonl" {
		t.Fatalf("want init with session file, got %+v", ev)
	}
	if sess.SessionID() != "/tmp/sess-1.jsonl" {
		t.Fatalf("SessionID() = %q", sess.SessionID())
	}
	// streaming deltas accumulate into text-so-far
	if ev := next(); ev.Kind != agent.EvTextDelta || ev.Text != "hel" {
		t.Fatalf("want first delta, got %+v", ev)
	}
	if ev := next(); ev.Kind != agent.EvTextDelta || ev.Text != "hello" {
		t.Fatalf("want accumulated delta, got %+v", ev)
	}
	// tool execution start becomes a tool event with a one-line args summary
	if ev := next(); ev.Kind != agent.EvToolUse || ev.Tool != "bash" || ev.Text != "command: ls -la" {
		t.Fatalf("want tool event, got %+v", ev)
	}
	// thinking blocks dropped, text blocks joined
	if ev := next(); ev.Kind != agent.EvText || ev.Text != "hello\nworld" {
		t.Fatalf("want text hello\\nworld, got %+v", ev)
	}
	// agent_end carries the last assistant text as the turn result
	if ev := next(); ev.Kind != agent.EvResult || ev.IsError || ev.Text != "hello\nworld" {
		t.Fatalf("want result, got %+v", ev)
	}
	// an errored assistant turn (empty content, errorMessage set) surfaces
	// as an error result at agent_end instead of vanishing
	if ev := next(); ev.Kind != agent.EvResult || !ev.IsError || ev.Text != "400 no extra usage" {
		t.Fatalf("want provider-error result, got %+v", ev)
	}
	// failed command response surfaces as an error result
	if ev := next(); ev.Kind != agent.EvResult || !ev.IsError || ev.Text != "prompt: boom" {
		t.Fatalf("want error result, got %+v", ev)
	}
	sess.Close()
	if ev := next(); ev.Kind != agent.EvExited {
		t.Fatalf("want exited, got %+v", ev)
	}
}

func TestResolveModel(t *testing.T) {
	for in, want := range map[string]string{
		"":                "claude-sonnet-5",
		"sonnet":          "claude-sonnet-5",
		"fable":           "claude-fable-5",
		"custom-model-id": "custom-model-id",
	} {
		if got := ResolveModel(in); got != want {
			t.Errorf("ResolveModel(%q) = %q, want %q", in, got, want)
		}
	}
}
