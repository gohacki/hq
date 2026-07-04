package rpc

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestCallAndEvents(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	srv := NewServer(sock)
	srv.Handle("echo", func(_ context.Context, params json.RawMessage) (any, error) {
		var p struct{ Msg string }
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return p.Msg + "!", nil
	})
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)

	cl, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	var out string
	if err := cl.Call("echo", map[string]string{"Msg": "hi"}, &out); err != nil {
		t.Fatal(err)
	}
	if out != "hi!" {
		t.Fatalf("got %q", out)
	}

	if err := cl.Call("nope", nil, nil); err == nil {
		t.Fatal("want error for unknown method")
	}

	if err := cl.Subscribe(); err != nil {
		t.Fatal(err)
	}
	srv.Publish("test.event", map[string]string{"a": "b"})
	select {
	case ev := <-cl.Events():
		if ev.Event != "test.event" {
			t.Fatalf("got event %q", ev.Event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
	}
}

func TestStaleSocketReplaced(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	s1 := NewServer(sock)
	if err := s1.Listen(); err != nil {
		t.Fatal(err)
	}
	s1.ln.Close() // dead listener leaves the socket file behind

	s2 := NewServer(sock)
	if err := s2.Listen(); err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	s2.ln.Close()
}
