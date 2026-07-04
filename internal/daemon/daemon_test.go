package daemon

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gohacki/shipyard/internal/config"
	"github.com/gohacki/shipyard/internal/rpc"
	"github.com/gohacki/shipyard/internal/store"
)

// bootDaemon starts a daemon on temp paths with no orchestrator and returns a
// connected client.
func bootDaemon(t *testing.T) (*Daemon, *rpc.Client) {
	t.Helper()
	dir := t.TempDir()
	paths := config.Paths{ConfigDir: filepath.Join(dir, "cfg"), DataDir: filepath.Join(dir, "data")}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := New(paths, st, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for {
		cl, err := rpc.Dial(paths.SocketPath())
		if err == nil {
			t.Cleanup(func() { cl.Close() })
			return d, cl
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not come up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func makeGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestDaemonEndToEnd(t *testing.T) {
	_, cl := bootDaemon(t)

	var pong string
	if err := cl.Call("ping", nil, &pong); err != nil || pong != "pong" {
		t.Fatalf("ping: %v %q", err, pong)
	}

	// home channel exists at boot
	var chs []ChannelView
	if err := cl.Call("channels.list", nil, &chs); err != nil {
		t.Fatal(err)
	}
	if len(chs) != 1 || chs[0].Name != HomeChannelName {
		t.Fatalf("want [home], got %+v", chs)
	}

	// create a channel spanning a real git repo
	repo := makeGitRepo(t)
	var ch store.Channel
	if err := cl.Call("channels.create", map[string]any{
		"name": "beta-os", "repos": []string{repo},
	}, &ch); err != nil {
		t.Fatal(err)
	}
	if ch.Delivery != "no-mistakes" {
		t.Fatalf("default delivery: %q", ch.Delivery)
	}
	if err := cl.Call("channels.list", nil, &chs); err != nil {
		t.Fatal(err)
	}
	if len(chs) != 2 {
		t.Fatalf("want 2 channels, got %+v", chs)
	}
	for _, c := range chs {
		if c.Name == "beta-os" && (len(c.Repos) != 1 || c.Repos[0].DefaultBranch != "main") {
			t.Fatalf("repo registration wrong: %+v", c.Repos)
		}
	}

	// instructions seeded and readable
	var instr struct{ Path, Body string }
	if err := cl.Call("instructions.get", map[string]any{"channel_id": ch.ID}, &instr); err != nil {
		t.Fatal(err)
	}
	if instr.Body == "" {
		t.Fatal("instructions not seeded")
	}

	// invalid inputs rejected
	if err := cl.Call("channels.create", map[string]any{"name": "Bad Name!"}, nil); err == nil {
		t.Fatal("want invalid-name error")
	}
	if err := cl.Call("channels.create", map[string]any{"name": "x", "delivery": "yolo"}, nil); err == nil {
		t.Fatal("want invalid-delivery error")
	}
	if err := cl.Call("channels.create", map[string]any{"name": "y", "repos": []string{t.TempDir()}}, nil); err == nil {
		t.Fatal("want not-a-git-repo error")
	}

	// message flow + events + unreads
	if err := cl.Subscribe(); err != nil {
		t.Fatal(err)
	}
	var sent store.Message
	if err := cl.Call("message.send", map[string]any{"channel_id": ch.ID, "body": "hello lead"}, &sent); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-cl.Events():
		if ev.Event != rpc.EvMessageNew {
			t.Fatalf("got event %q", ev.Event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message.new event")
	}
	var msgs []store.Message
	if err := cl.Call("messages.list", map[string]any{"channel_id": ch.ID}, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Author != "captain" {
		t.Fatalf("messages: %+v", msgs)
	}
	if err := cl.Call("message.send", map[string]any{"channel_id": ch.ID, "body": "   "}, nil); err == nil {
		t.Fatal("want empty-message error")
	}
}
