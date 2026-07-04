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

func TestChannelRoundTrip(t *testing.T) {
	s := testStore(t)
	ch := Channel{ID: NewID("ch"), Name: "beta-os", Delivery: "no-mistakes"}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	got, err := s.ChannelByName("beta-os")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ch.ID || got.Delivery != "no-mistakes" {
		t.Fatalf("got %+v", got)
	}
	if _, err := s.ChannelByName("nope"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestTaskLifecycle(t *testing.T) {
	s := testStore(t)
	ch := Channel{ID: NewID("ch"), Name: "c"}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	task := Task{ID: NewID("tsk"), ChannelID: ch.ID, Kind: "ship", Title: "fix login"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	got, err := s.TaskByID(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != TaskQueued {
		t.Fatalf("want queued, got %s", got.Status)
	}
	got.Status = TaskRunning
	got.SessionID = "sess-1"
	if err := s.UpdateTask(got); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].SessionID != "sess-1" {
		t.Fatalf("active: %+v", active)
	}
	got.Status = TaskDone
	if err := s.UpdateTask(got); err != nil {
		t.Fatal(err)
	}
	if active, _ = s.ActiveTasks(); len(active) != 0 {
		t.Fatalf("want no active tasks, got %+v", active)
	}
}

func TestMessagesAndUnreads(t *testing.T) {
	s := testStore(t)
	ch := Channel{ID: NewID("ch"), Name: "c"}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(Message{ChannelID: ch.ID, Author: "captain", Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	id2, err := s.AppendMessage(Message{ChannelID: ch.ID, Author: "lead", Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Messages(ch.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Body != "hi" || msgs[1].Body != "hello" {
		t.Fatalf("msgs: %+v", msgs)
	}

	// captain's own message never counts as unread
	n, err := s.UnreadCount(ch.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 unread, got %d", n)
	}
	if err := s.MarkRead(ch.ID, "", id2); err != nil {
		t.Fatal(err)
	}
	if n, _ = s.UnreadCount(ch.ID, ""); n != 0 {
		t.Fatalf("want 0 unread, got %d", n)
	}

	// thread scope is independent of channel scope
	if _, err := s.AppendMessage(Message{ChannelID: ch.ID, TaskID: "tsk_1", Author: "crew:tsk_1", Body: "working"}); err != nil {
		t.Fatal(err)
	}
	if n, _ = s.UnreadCount(ch.ID, "tsk_1"); n != 1 {
		t.Fatalf("want 1 thread unread, got %d", n)
	}
	if n, _ = s.UnreadCount(ch.ID, ""); n != 0 {
		t.Fatalf("channel unreads leaked from thread: %d", n)
	}
}

func TestMarkReadNeverRegresses(t *testing.T) {
	s := testStore(t)
	ch := Channel{ID: NewID("ch"), Name: "c"}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	id1, _ := s.AppendMessage(Message{ChannelID: ch.ID, Author: "lead", Body: "a"})
	id2, _ := s.AppendMessage(Message{ChannelID: ch.ID, Author: "lead", Body: "b"})
	if err := s.MarkRead(ch.ID, "", id2); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRead(ch.ID, "", id1); err != nil { // stale cursor must not regress
		t.Fatal(err)
	}
	if n, _ := s.UnreadCount(ch.ID, ""); n != 0 {
		t.Fatalf("read cursor regressed: %d unread", n)
	}
}
