package orch

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gohacki/hq/internal/agent"
	"github.com/gohacki/hq/internal/config"
	"github.com/gohacki/hq/internal/daemon"
	"github.com/gohacki/hq/internal/store"
)

// fakeHarness records Start specs and hands out inert sessions.
type fakeHarness struct {
	mu     sync.Mutex
	specs  []agent.Spec
	events chan agent.Event
}

func (f *fakeHarness) Start(ctx context.Context, spec agent.Spec) (agent.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	return &fakeSession{events: f.events}, nil
}

func (f *fakeHarness) InteractiveCommand(sessionID, model string, extra ...string) []string {
	return []string{"fake"}
}

type fakeSession struct{ events chan agent.Event }

func (s *fakeSession) SessionID() string          { return "" }
func (s *fakeSession) Send(string) error          { return nil }
func (s *fakeSession) Events() <-chan agent.Event { return s.events }
func (s *fakeSession) Interrupt() error           { return nil }
func (s *fakeSession) Close() error               { return nil }

func TestReconcileResumesRunningEngineers(t *testing.T) {
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
	d := daemon.New(paths, st, slog.New(slog.DiscardHandler))
	fh := &fakeHarness{events: make(chan agent.Event)}
	defer close(fh.events)
	o := New(d, fh, slog.New(slog.DiscardHandler))
	d.Orch = o

	if err := st.CreateProject(store.Project{ID: "p1", Name: "proj"}); err != nil {
		t.Fatal(err)
	}
	mk := func(id string, status store.TicketStatus, sessionID, worktree string) store.Ticket {
		tk := store.Ticket{
			ID: id, ProjectID: "p1", Kind: "build", Title: id,
			Status: status, SessionID: sessionID, WorktreePath: worktree,
		}
		if err := st.CreateTicket(tk); err != nil {
			t.Fatal(err)
		}
		return tk
	}
	resumable := mk("tkt-resume", store.TicketRunning, "sess-1", dir)
	noSession := mk("tkt-nosess", store.TicketRunning, "", dir)
	visiting := mk("tkt-visit", store.TicketVisiting, "sess-2", dir)
	waiting := mk("tkt-wait", store.TicketNeedsInput, "sess-3", dir)

	o.Reconcile(context.Background(), []store.Ticket{resumable, noSession, visiting, waiting})

	fh.mu.Lock()
	if len(fh.specs) != 1 {
		t.Fatalf("want exactly one resumed engineer, got %d specs", len(fh.specs))
	}
	spec := fh.specs[0]
	fh.mu.Unlock()
	if spec.ResumeSessionID != "sess-1" || spec.WorkDir != dir {
		t.Fatalf("resume spec wrong: %+v", spec)
	}
	if spec.Prompt != restartResumePrompt {
		t.Fatalf("resume prompt wrong: %q", spec.Prompt)
	}

	want := map[string]store.TicketStatus{
		"tkt-resume": store.TicketRunning,    // resumed in place
		"tkt-nosess": store.TicketNeedsInput, // nothing to resume — parked
		"tkt-visit":  store.TicketNeedsInput, // visiting always parks
		"tkt-wait":   store.TicketNeedsInput, // untouched
	}
	for id, status := range want {
		got, err := st.TicketByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != status {
			t.Errorf("%s: want %s, got %s", id, status, got.Status)
		}
	}
}
