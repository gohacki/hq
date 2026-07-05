package store

import (
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestProjectRoundTrip(t *testing.T) {
	s := testStore(t)
	p := Project{ID: NewID("prj"), Name: "beta-os", Delivery: "no-mistakes"}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProjectByName("beta-os")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != p.ID || got.Delivery != "no-mistakes" || got.Verify != "on-completion" {
		t.Fatalf("got %+v", got)
	}
	if _, err := s.ProjectByName("nope"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestTicketLifecycle(t *testing.T) {
	s := testStore(t)
	p := Project{ID: NewID("prj"), Name: "p"}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	tk := Ticket{ID: NewID("tkt"), ProjectID: p.ID, Kind: "build", Title: "fix login"}
	if err := s.CreateTicket(tk); err != nil {
		t.Fatal(err)
	}
	got, err := s.TicketByID(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != TicketQueued {
		t.Fatalf("want queued, got %s", got.Status)
	}
	got.Status = TicketRunning
	got.SessionID = "sess-1"
	if err := s.UpdateTicket(got); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveTickets()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].SessionID != "sess-1" {
		t.Fatalf("active: %+v", active)
	}
	got.Status = TicketDone
	got.WorktreePath = "/tmp/wt"
	if err := s.UpdateTicket(got); err != nil {
		t.Fatal(err)
	}
	if active, _ = s.ActiveTickets(); len(active) != 0 {
		t.Fatalf("want no active tickets, got %+v", active)
	}
	sweep, err := s.DoneTicketsWithWorktree()
	if err != nil || len(sweep) != 1 {
		t.Fatalf("teardown sweep: %v %+v", err, sweep)
	}
}

func TestMessagesAndUnreads(t *testing.T) {
	s := testStore(t)
	p := Project{ID: NewID("prj"), Name: "p"}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(Message{ProjectID: p.ID, Author: "boss", Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	id2, err := s.AppendMessage(Message{ProjectID: p.ID, Author: "em", Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Messages(p.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Body != "hi" || msgs[1].Body != "hello" {
		t.Fatalf("msgs: %+v", msgs)
	}

	// boss's own message never counts as unread
	n, err := s.UnreadCount(p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 unread, got %d", n)
	}
	if err := s.MarkRead(p.ID, "", id2); err != nil {
		t.Fatal(err)
	}
	if n, _ = s.UnreadCount(p.ID, ""); n != 0 {
		t.Fatalf("want 0 unread, got %d", n)
	}

	// ticket scope is independent of project scope
	if _, err := s.AppendMessage(Message{ProjectID: p.ID, TicketID: "tkt_1", Author: "eng:tkt_1", Body: "working"}); err != nil {
		t.Fatal(err)
	}
	if n, _ = s.UnreadCount(p.ID, "tkt_1"); n != 1 {
		t.Fatalf("want 1 thread unread, got %d", n)
	}
	if n, _ = s.UnreadCount(p.ID, ""); n != 0 {
		t.Fatalf("project unreads leaked from ticket: %d", n)
	}
}

func TestItemsLifecycle(t *testing.T) {
	s := testStore(t)
	itDemo, err := s.CreateItem(Item{Kind: ItemDemo, Tier: TierBreak, ProjectID: "prj_1", TicketID: "tkt_1", Title: "demo ready"})
	if err != nil {
		t.Fatal(err)
	}
	itQ, err := s.CreateItem(Item{Kind: ItemQuestion, Tier: TierInterrupt, ProjectID: "prj_1", TicketID: "tkt_2", Title: "cap retries?"})
	if err != nil {
		t.Fatal(err)
	}

	open, err := s.OpenItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 || open[0].ID != itQ.ID {
		t.Fatalf("interrupts must sort first: %+v", open)
	}

	if err := s.ResolveItem(itQ.ID); err != nil {
		t.Fatal(err)
	}
	open, _ = s.OpenItems()
	if len(open) != 1 || open[0].ID != itDemo.ID {
		t.Fatalf("after resolve: %+v", open)
	}

	// resolving by ticket clears everything attached to it
	ids, err := s.ResolveTicketItems("tkt_1")
	if err != nil || len(ids) != 1 {
		t.Fatalf("ResolveTicketItems: %v %v", err, ids)
	}
	if open, _ = s.OpenItems(); len(open) != 0 {
		t.Fatalf("want empty office, got %+v", open)
	}
}

func TestPlansAndSettings(t *testing.T) {
	s := testStore(t)
	prj := Project{ID: NewID("prj"), Name: "p"}
	if err := s.CreateProject(prj); err != nil {
		t.Fatal(err)
	}
	pl, err := s.CreatePlan(Plan{ProjectID: prj.ID, DocPath: "/tmp/plan.md", DocMD: "# plan", Tickets: `[{"title":"a"}]`})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PlanByID(pl.ID)
	if err != nil || got.Status != PlanPending {
		t.Fatalf("plan: %v %+v", err, got)
	}
	if err := s.SetPlanStatus(pl.ID, PlanApproved); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.PlanByID(pl.ID); got.Status != PlanApproved {
		t.Fatalf("status: %+v", got)
	}

	v, err := s.GetSetting("presence", "available")
	if err != nil || v != "available" {
		t.Fatalf("default setting: %v %q", err, v)
	}
	if err := s.SetSetting("presence", "heads-down"); err != nil {
		t.Fatal(err)
	}
	if v, _ = s.GetSetting("presence", "available"); v != "heads-down" {
		t.Fatalf("setting: %q", v)
	}
}
