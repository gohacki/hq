package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gohacki/hq/internal/config"
	"github.com/gohacki/hq/internal/rpc"
	"github.com/gohacki/hq/internal/store"
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
	d, cl := bootDaemon(t)

	var pong string
	if err := cl.Call("ping", nil, &pong); err != nil || pong != "pong" {
		t.Fatalf("ping: %v %q", err, pong)
	}

	// director room exists at boot
	var prjs []ProjectView
	if err := cl.Call("projects.list", nil, &prjs); err != nil {
		t.Fatal(err)
	}
	if len(prjs) != 1 || prjs[0].Name != store.DirectorRoomName {
		t.Fatalf("want [director], got %+v", prjs)
	}

	// create a project spanning a real git repo
	repo := makeGitRepo(t)
	var prj store.Project
	if err := cl.Call("projects.create", map[string]any{
		"name": "beta-os", "repos": []string{repo},
	}, &prj); err != nil {
		t.Fatal(err)
	}
	if prj.Delivery != "no-mistakes" || prj.Verify != "on-completion" {
		t.Fatalf("defaults: %+v", prj)
	}
	if err := cl.Call("projects.list", nil, &prjs); err != nil {
		t.Fatal(err)
	}
	if len(prjs) != 2 {
		t.Fatalf("want 2 projects, got %+v", prjs)
	}
	for _, p := range prjs {
		if p.Name == "beta-os" && (len(p.Repos) != 1 || p.Repos[0].DefaultBranch != "main") {
			t.Fatalf("repo registration wrong: %+v", p.Repos)
		}
	}

	// handbook seeded and readable
	var hb struct{ Path, Body string }
	if err := cl.Call("handbook.get", map[string]any{"project_id": prj.ID}, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.Body == "" {
		t.Fatal("handbook not seeded")
	}

	// invalid inputs rejected
	if err := cl.Call("projects.create", map[string]any{"name": "Bad Name!"}, nil); err == nil {
		t.Fatal("want invalid-name error")
	}
	if err := cl.Call("projects.create", map[string]any{"name": "x", "delivery": "yolo"}, nil); err == nil {
		t.Fatal("want invalid-delivery error")
	}
	if err := cl.Call("projects.create", map[string]any{"name": "x", "verify": "sometimes"}, nil); err == nil {
		t.Fatal("want invalid-verify error")
	}

	// message flow + events + unreads
	if err := cl.Subscribe(); err != nil {
		t.Fatal(err)
	}
	var sent store.Message
	if err := cl.Call("message.send", map[string]any{"project_id": prj.ID, "body": "hello em"}, &sent); err != nil {
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

	// items engine: file → list → resolve, with events
	it, err := d.FileItem(store.Item{
		Kind: store.ItemQuestion, Tier: store.TierInterrupt,
		ProjectID: prj.ID, Title: "cap retries?",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-cl.Events():
		if ev.Event != rpc.EvItemNew {
			t.Fatalf("got event %q", ev.Event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no item.new event")
	}
	var items []store.Item
	if err := cl.Call("items.list", nil, &items); err != nil || len(items) != 1 {
		t.Fatalf("items.list: %v %+v", err, items)
	}
	if err := cl.Call("item.resolve", map[string]any{"id": it.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := cl.Call("items.list", nil, &items); err != nil || len(items) != 0 {
		t.Fatalf("after resolve: %v %+v", err, items)
	}

	// items auto-resolve when their ticket starts running again
	if err := d.Store.CreateTicket(store.Ticket{ID: "tkt_x", ProjectID: prj.ID, Kind: "build", Title: "x", Status: store.TicketNeedsInput}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.FileItem(store.Item{Kind: store.ItemQuestion, Tier: store.TierInterrupt, ProjectID: prj.ID, TicketID: "tkt_x", Title: "q"}); err != nil {
		t.Fatal(err)
	}
	tk, _ := d.Store.TicketByID("tkt_x")
	tk.Status = store.TicketRunning
	if err := d.UpdateTicket(tk); err != nil {
		t.Fatal(err)
	}
	if err := cl.Call("items.list", nil, &items); err != nil || len(items) != 0 {
		t.Fatalf("ticket items not auto-resolved: %v %+v", err, items)
	}

	// presence
	var mode string
	if err := cl.Call("presence.get", nil, &mode); err != nil || mode != "available" {
		t.Fatalf("presence default: %v %q", err, mode)
	}
	if err := cl.Call("presence.set", map[string]any{"mode": "heads-down"}, &mode); err != nil || mode != "heads-down" {
		t.Fatalf("presence.set: %v %q", err, mode)
	}
	if err := cl.Call("presence.set", map[string]any{"mode": "napping"}, nil); err == nil {
		t.Fatal("want invalid-mode error")
	}
}

func TestDirectorRoomMigration(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{ConfigDir: filepath.Join(dir, "cfg"), DataDir: filepath.Join(dir, "data")}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := New(paths, st, slog.New(slog.DiscardHandler))

	// A pre-rename daemon left a conference-room row with history and a data dir.
	oldDir := paths.ProjectDir(legacyDirectorRoomName)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "handbook.md"), []byte("legacy handbook"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{
		ID: "p-legacy", Name: legacyDirectorRoomName, Delivery: "local-only", Verify: "none",
		HandbookPath: filepath.Join(oldDir, "handbook.md"), EMSessionID: "sess-keep",
	}); err != nil {
		t.Fatal(err)
	}

	if err := d.ensureDirectorRoom(); err != nil {
		t.Fatal(err)
	}
	p, err := st.ProjectByName(store.DirectorRoomName)
	if err != nil {
		t.Fatalf("director project missing after migration: %v", err)
	}
	if p.ID != "p-legacy" || p.EMSessionID != "sess-keep" {
		t.Fatalf("migration must keep the same row (id, session), got %+v", p)
	}
	wantHandbook := filepath.Join(paths.ProjectDir(store.DirectorRoomName), "handbook.md")
	if p.HandbookPath != wantHandbook {
		t.Fatalf("handbook path not migrated: %q", p.HandbookPath)
	}
	if b, err := os.ReadFile(wantHandbook); err != nil || string(b) != "legacy handbook" {
		t.Fatalf("handbook content lost: %v %q", err, b)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("legacy dir should be gone: %v", err)
	}
	if _, err := st.ProjectByName(legacyDirectorRoomName); err != store.ErrNotFound {
		t.Fatalf("legacy row should be renamed away, got %v", err)
	}

	// Idempotent: a second boot leaves the single migrated row alone.
	if err := d.ensureDirectorRoom(); err != nil {
		t.Fatal(err)
	}
	prjs, err := st.Projects()
	if err != nil || len(prjs) != 1 {
		t.Fatalf("want exactly one project after re-run, got %d (%v)", len(prjs), err)
	}
}
