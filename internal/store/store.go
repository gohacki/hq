// Package store is shipyard's SQLite persistence layer. The daemon is the
// only writer; the TUI reads via daemon RPC, never the database directly.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS channels (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL UNIQUE,
  delivery          TEXT NOT NULL DEFAULT 'no-mistakes',
  instructions_path TEXT NOT NULL DEFAULT '',
  lead_session_id   TEXT NOT NULL DEFAULT '',
  created_at        INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS repos (
  id             TEXT PRIMARY KEY,
  channel_id     TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  path           TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT 'main',
  UNIQUE(channel_id, name)
);
CREATE TABLE IF NOT EXISTS tasks (
  id            TEXT PRIMARY KEY,
  channel_id    TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  repo_id       TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL CHECK (kind IN ('ship','scout')),
  title         TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'queued',
  branch        TEXT NOT NULL DEFAULT '',
  worktree_path TEXT NOT NULL DEFAULT '',
  session_id    TEXT NOT NULL DEFAULT '',
  brief_path    TEXT NOT NULL DEFAULT '',
  report_path   TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  task_id    TEXT NOT NULL DEFAULT '',
  author     TEXT NOT NULL,             -- 'captain' | 'lead' | 'crew:<task-id>' | 'system'
  kind       TEXT NOT NULL DEFAULT 'text', -- text | report | gate | system
  body       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_scope ON messages(channel_id, task_id, id);
CREATE TABLE IF NOT EXISTS reads (
  scope_id             TEXT PRIMARY KEY,  -- channel id, or channel:task for threads
  last_read_message_id INTEGER NOT NULL DEFAULT 0
);
`

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

var ErrNotFound = errors.New("not found")

func now() int64 { return time.Now().Unix() }

// --- channels ---

type Channel struct {
	ID               string
	Name             string
	Delivery         string
	InstructionsPath string
	LeadSessionID    string
	CreatedAt        int64
}

func (s *Store) CreateChannel(c Channel) error {
	c.CreatedAt = now()
	_, err := s.db.Exec(`INSERT INTO channels (id, name, delivery, instructions_path, lead_session_id, created_at) VALUES (?,?,?,?,?,?)`,
		c.ID, c.Name, c.Delivery, c.InstructionsPath, c.LeadSessionID, c.CreatedAt)
	return err
}

func (s *Store) Channels() ([]Channel, error) {
	rows, err := s.db.Query(`SELECT id, name, delivery, instructions_path, lead_session_id, created_at FROM channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.Name, &c.Delivery, &c.InstructionsPath, &c.LeadSessionID, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ChannelByName(name string) (Channel, error) {
	var c Channel
	err := s.db.QueryRow(`SELECT id, name, delivery, instructions_path, lead_session_id, created_at FROM channels WHERE name = ?`, name).
		Scan(&c.ID, &c.Name, &c.Delivery, &c.InstructionsPath, &c.LeadSessionID, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) ChannelByID(id string) (Channel, error) {
	var c Channel
	err := s.db.QueryRow(`SELECT id, name, delivery, instructions_path, lead_session_id, created_at FROM channels WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Delivery, &c.InstructionsPath, &c.LeadSessionID, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) SetChannelLeadSession(id, sessionID string) error {
	_, err := s.db.Exec(`UPDATE channels SET lead_session_id = ? WHERE id = ?`, sessionID, id)
	return err
}

func (s *Store) SetChannelDelivery(id, delivery string) error {
	_, err := s.db.Exec(`UPDATE channels SET delivery = ? WHERE id = ?`, delivery, id)
	return err
}

// --- repos ---

type Repo struct {
	ID            string
	ChannelID     string
	Name          string
	Path          string
	DefaultBranch string
}

func (s *Store) AddRepo(r Repo) error {
	_, err := s.db.Exec(`INSERT INTO repos (id, channel_id, name, path, default_branch) VALUES (?,?,?,?,?)`,
		r.ID, r.ChannelID, r.Name, r.Path, r.DefaultBranch)
	return err
}

func (s *Store) ReposForChannel(channelID string) ([]Repo, error) {
	rows, err := s.db.Query(`SELECT id, channel_id, name, path, default_branch FROM repos WHERE channel_id = ? ORDER BY name`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.ChannelID, &r.Name, &r.Path, &r.DefaultBranch); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- tasks ---

type TaskStatus string

const (
	TaskQueued     TaskStatus = "queued"
	TaskRunning    TaskStatus = "running"
	TaskNeedsInput TaskStatus = "needs-input"
	TaskBlocked    TaskStatus = "blocked"
	TaskDelivering TaskStatus = "delivering"
	TaskAttached   TaskStatus = "attached" // escape hatch open
	TaskDone       TaskStatus = "done"
	TaskFailed     TaskStatus = "failed"
	TaskAbandoned  TaskStatus = "abandoned"
)

// Terminal reports whether no further supervision applies.
func (ts TaskStatus) Terminal() bool {
	return ts == TaskDone || ts == TaskFailed || ts == TaskAbandoned
}

type Task struct {
	ID           string
	ChannelID    string
	RepoID       string
	Kind         string // ship | scout
	Title        string
	Status       TaskStatus
	Branch       string
	WorktreePath string
	SessionID    string
	BriefPath    string
	ReportPath   string
	CreatedAt    int64
	UpdatedAt    int64
}

func (s *Store) CreateTask(t Task) error {
	t.CreatedAt, t.UpdatedAt = now(), now()
	if t.Status == "" {
		t.Status = TaskQueued
	}
	_, err := s.db.Exec(`INSERT INTO tasks (id, channel_id, repo_id, kind, title, status, branch, worktree_path, session_id, brief_path, report_path, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.ChannelID, t.RepoID, t.Kind, t.Title, t.Status, t.Branch, t.WorktreePath, t.SessionID, t.BriefPath, t.ReportPath, t.CreatedAt, t.UpdatedAt)
	return err
}

func (s *Store) UpdateTask(t Task) error {
	t.UpdatedAt = now()
	_, err := s.db.Exec(`UPDATE tasks SET status=?, branch=?, worktree_path=?, session_id=?, brief_path=?, report_path=?, updated_at=? WHERE id=?`,
		t.Status, t.Branch, t.WorktreePath, t.SessionID, t.BriefPath, t.ReportPath, t.UpdatedAt, t.ID)
	return err
}

func (s *Store) TaskByID(id string) (Task, error) {
	var t Task
	err := s.db.QueryRow(`SELECT id, channel_id, repo_id, kind, title, status, branch, worktree_path, session_id, brief_path, report_path, created_at, updated_at FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &t.ChannelID, &t.RepoID, &t.Kind, &t.Title, &t.Status, &t.Branch, &t.WorktreePath, &t.SessionID, &t.BriefPath, &t.ReportPath, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) TasksForChannel(channelID string) ([]Task, error) {
	rows, err := s.db.Query(`SELECT id, channel_id, repo_id, kind, title, status, branch, worktree_path, session_id, brief_path, report_path, created_at, updated_at
		FROM tasks WHERE channel_id = ? ORDER BY created_at DESC`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.ChannelID, &t.RepoID, &t.Kind, &t.Title, &t.Status, &t.Branch, &t.WorktreePath, &t.SessionID, &t.BriefPath, &t.ReportPath, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ActiveTasks returns all non-terminal tasks across channels (daemon boot reconcile).
func (s *Store) ActiveTasks() ([]Task, error) {
	rows, err := s.db.Query(`SELECT id, channel_id, repo_id, kind, title, status, branch, worktree_path, session_id, brief_path, report_path, created_at, updated_at
		FROM tasks WHERE status NOT IN ('done','failed','abandoned')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.ChannelID, &t.RepoID, &t.Kind, &t.Title, &t.Status, &t.Branch, &t.WorktreePath, &t.SessionID, &t.BriefPath, &t.ReportPath, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- messages ---

type Message struct {
	ID        int64
	ChannelID string
	TaskID    string // "" for main channel scroll
	Author    string // captain | lead | crew:<task-id> | system
	Kind      string // text | report | gate | system
	Body      string
	CreatedAt int64
}

func (s *Store) AppendMessage(m Message) (int64, error) {
	m.CreatedAt = now()
	if m.Kind == "" {
		m.Kind = "text"
	}
	res, err := s.db.Exec(`INSERT INTO messages (channel_id, task_id, author, kind, body, created_at) VALUES (?,?,?,?,?,?)`,
		m.ChannelID, m.TaskID, m.Author, m.Kind, m.Body, m.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Messages returns the scroll for a channel (taskID == "") or thread, oldest first.
func (s *Store) Messages(channelID, taskID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT id, channel_id, task_id, author, kind, body, created_at FROM (
			SELECT * FROM messages WHERE channel_id = ? AND task_id = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC`, channelID, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.TaskID, &m.Author, &m.Kind, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- reads / unread badges ---

func scopeID(channelID, taskID string) string {
	if taskID == "" {
		return channelID
	}
	return channelID + ":" + taskID
}

func (s *Store) MarkRead(channelID, taskID string, lastMessageID int64) error {
	_, err := s.db.Exec(`INSERT INTO reads (scope_id, last_read_message_id) VALUES (?,?)
		ON CONFLICT(scope_id) DO UPDATE SET last_read_message_id = MAX(last_read_message_id, excluded.last_read_message_id)`,
		scopeID(channelID, taskID), lastMessageID)
	return err
}

// UnreadCount counts messages after the read cursor, excluding the captain's own.
func (s *Store) UnreadCount(channelID, taskID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages
		WHERE channel_id = ? AND task_id = ? AND author != 'captain'
		AND id > COALESCE((SELECT last_read_message_id FROM reads WHERE scope_id = ?), 0)`,
		channelID, taskID, scopeID(channelID, taskID)).Scan(&n)
	return n, err
}

// NewID returns a short random identifier with the given prefix, e.g. "ch_ab12cd34".
func NewID(prefix string) string {
	return fmt.Sprintf("%s_%08x", prefix, randUint32())
}
