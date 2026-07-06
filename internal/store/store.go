// Package store is hq's SQLite persistence layer. The daemon is the only
// writer; the TUI reads via daemon RPC, never the database directly.
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
CREATE TABLE IF NOT EXISTS projects (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,
  delivery        TEXT NOT NULL DEFAULT 'no-mistakes',
  verify          TEXT NOT NULL DEFAULT 'on-completion',
  em_model        TEXT NOT NULL DEFAULT '',
  eng_model       TEXT NOT NULL DEFAULT '',
  handbook_path   TEXT NOT NULL DEFAULT '',
  em_session_id   TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS repos (
  id             TEXT PRIMARY KEY,
  project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  path           TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT 'main',
  UNIQUE(project_id, name)
);
CREATE TABLE IF NOT EXISTS tickets (
  id            TEXT PRIMARY KEY,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  repo_id       TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL CHECK (kind IN ('build','spike')),
  title         TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'queued',
  model         TEXT NOT NULL DEFAULT '',
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
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ticket_id  TEXT NOT NULL DEFAULT '',
  author     TEXT NOT NULL,               -- 'boss' | 'pm' | 'em' | 'eng:<ticket-id>' | 'system'
  kind       TEXT NOT NULL DEFAULT 'text', -- text | report | gate | system
  body       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_scope ON messages(project_id, ticket_id, id);
CREATE TABLE IF NOT EXISTS reads (
  scope_id             TEXT PRIMARY KEY,  -- project id, or project:ticket
  last_read_message_id INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS items (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,  -- question | options | demo | plan | blocked | failed | handbook
  tier        TEXT NOT NULL,  -- interrupt | break
  project_id  TEXT NOT NULL,
  ticket_id   TEXT NOT NULL DEFAULT '',
  ref_id      TEXT NOT NULL DEFAULT '',   -- plan id / handbook-proposal id when applicable
  title       TEXT NOT NULL,
  body        TEXT NOT NULL DEFAULT '',
  options     TEXT NOT NULL DEFAULT '',   -- JSON array for kind=options
  created_at  INTEGER NOT NULL,
  resolved_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_items_open ON items(resolved_at, tier, created_at);
CREATE TABLE IF NOT EXISTS plans (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  doc_path    TEXT NOT NULL,
  doc_md      TEXT NOT NULL,
  tickets     TEXT NOT NULL,              -- JSON array of proposed tickets
  questions   TEXT NOT NULL DEFAULT '',   -- JSON array of open questions
  status      TEXT NOT NULL DEFAULT 'pending', -- pending | approved | rejected
  created_at  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS worktrees (
  id         TEXT PRIMARY KEY,
  ticket_id  TEXT NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  repo_id    TEXT NOT NULL DEFAULT '',
  repo_path  TEXT NOT NULL DEFAULT '',
  path       TEXT NOT NULL,
  branch     TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_worktrees_ticket ON worktrees(ticket_id);
`

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

var ErrNotFound = errors.New("not found")

func now() int64 { return time.Now().Unix() }

// --- projects ---

type Project struct {
	ID           string
	Name         string
	Delivery     string
	Verify       string // none | before-delivery | on-completion
	EMModel      string // "" = harness default (sonnet)
	EngModel     string // "" = harness default (sonnet)
	HandbookPath string
	EMSessionID  string
	CreatedAt    int64
}

const projectCols = `id, name, delivery, verify, em_model, eng_model, handbook_path, em_session_id, created_at`

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.Delivery, &p.Verify, &p.EMModel, &p.EngModel, &p.HandbookPath, &p.EMSessionID, &p.CreatedAt)
	return p, err
}

func (s *Store) CreateProject(p Project) error {
	p.CreatedAt = now()
	if p.Verify == "" {
		p.Verify = "on-completion"
	}
	_, err := s.db.Exec(`INSERT INTO projects (`+projectCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Delivery, p.Verify, p.EMModel, p.EngModel, p.HandbookPath, p.EMSessionID, p.CreatedAt)
	return err
}

func (s *Store) Projects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ProjectByName(name string) (Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) ProjectByID(id string) (Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// DeleteProject removes a project and everything scoped to it: repos,
// tickets, messages, and plans cascade via foreign keys (schema has
// ON DELETE CASCADE + foreign_keys=ON in the DSN); items and reads have no
// FK (items has no constraint at all, and reads is keyed by a scope string,
// not project_id) so they're deleted explicitly in the same transaction.
// Callers are responsible for anything outside the DB — live agent
// processes, leased worktrees, on-disk project files.
func (s *Store) DeleteProject(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM items WHERE project_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM reads WHERE scope_id = ? OR scope_id LIKE ?`, id, id+":%"); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) SetProjectEMSession(id, sessionID string) error {
	_, err := s.db.Exec(`UPDATE projects SET em_session_id = ? WHERE id = ?`, sessionID, id)
	return err
}

func (s *Store) SetProjectDelivery(id, delivery string) error {
	_, err := s.db.Exec(`UPDATE projects SET delivery = ? WHERE id = ?`, delivery, id)
	return err
}

func (s *Store) SetProjectVerify(id, verify string) error {
	_, err := s.db.Exec(`UPDATE projects SET verify = ? WHERE id = ?`, verify, id)
	return err
}

func (s *Store) SetProjectEMModel(id, model string) error {
	_, err := s.db.Exec(`UPDATE projects SET em_model = ? WHERE id = ?`, model, id)
	return err
}

func (s *Store) SetProjectEngModel(id, model string) error {
	_, err := s.db.Exec(`UPDATE projects SET eng_model = ? WHERE id = ?`, model, id)
	return err
}

// --- repos ---

type Repo struct {
	ID            string
	ProjectID     string
	Name          string
	Path          string
	DefaultBranch string
}

func (s *Store) AddRepo(r Repo) error {
	_, err := s.db.Exec(`INSERT INTO repos (id, project_id, name, path, default_branch) VALUES (?,?,?,?,?)`,
		r.ID, r.ProjectID, r.Name, r.Path, r.DefaultBranch)
	return err
}

func (s *Store) ReposForProject(projectID string) ([]Repo, error) {
	rows, err := s.db.Query(`SELECT id, project_id, name, path, default_branch FROM repos WHERE project_id = ? ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Name, &r.Path, &r.DefaultBranch); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- tickets ---

type TicketStatus string

const (
	TicketQueued     TicketStatus = "queued"
	TicketRunning    TicketStatus = "running"
	TicketNeedsInput TicketStatus = "needs-input"
	TicketBlocked    TicketStatus = "blocked"
	TicketDelivering TicketStatus = "delivering"
	TicketVisiting   TicketStatus = "visiting" // desk visit open
	TicketDone       TicketStatus = "done"
	TicketFailed     TicketStatus = "failed"
	TicketAbandoned  TicketStatus = "abandoned"
)

// Terminal reports whether no further supervision applies.
func (ts TicketStatus) Terminal() bool {
	return ts == TicketDone || ts == TicketFailed || ts == TicketAbandoned
}

type Ticket struct {
	ID           string
	ProjectID    string
	RepoID       string
	Kind         string // build | spike
	Title        string
	Status       TicketStatus
	Model        string // "" = project eng model
	Branch       string
	WorktreePath string
	SessionID    string
	BriefPath    string
	ReportPath   string
	CreatedAt    int64
	UpdatedAt    int64
}

const ticketCols = `id, project_id, repo_id, kind, title, status, model, branch, worktree_path, session_id, brief_path, report_path, created_at, updated_at`

func scanTicket(row interface{ Scan(...any) error }) (Ticket, error) {
	var t Ticket
	err := row.Scan(&t.ID, &t.ProjectID, &t.RepoID, &t.Kind, &t.Title, &t.Status, &t.Model, &t.Branch, &t.WorktreePath, &t.SessionID, &t.BriefPath, &t.ReportPath, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *Store) queryTickets(query string, args ...any) ([]Ticket, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ticket
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CreateTicket(t Ticket) error {
	t.CreatedAt, t.UpdatedAt = now(), now()
	if t.Status == "" {
		t.Status = TicketQueued
	}
	_, err := s.db.Exec(`INSERT INTO tickets (`+ticketCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.ProjectID, t.RepoID, t.Kind, t.Title, t.Status, t.Model, t.Branch, t.WorktreePath, t.SessionID, t.BriefPath, t.ReportPath, t.CreatedAt, t.UpdatedAt)
	return err
}

func (s *Store) UpdateTicket(t Ticket) error {
	t.UpdatedAt = now()
	_, err := s.db.Exec(`UPDATE tickets SET status=?, model=?, branch=?, worktree_path=?, session_id=?, brief_path=?, report_path=?, updated_at=? WHERE id=?`,
		t.Status, t.Model, t.Branch, t.WorktreePath, t.SessionID, t.BriefPath, t.ReportPath, t.UpdatedAt, t.ID)
	return err
}

func (s *Store) TicketByID(id string) (Ticket, error) {
	t, err := scanTicket(s.db.QueryRow(`SELECT `+ticketCols+` FROM tickets WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) TicketsForProject(projectID string) ([]Ticket, error) {
	return s.queryTickets(`SELECT `+ticketCols+` FROM tickets WHERE project_id = ? ORDER BY created_at DESC`, projectID)
}

// AllTickets returns every ticket (PM cross-project view, board).
func (s *Store) AllTickets() ([]Ticket, error) {
	return s.queryTickets(`SELECT ` + ticketCols + ` FROM tickets ORDER BY updated_at DESC`)
}

// ActiveTickets returns all non-terminal tickets (daemon boot reconcile).
func (s *Store) ActiveTickets() ([]Ticket, error) {
	return s.queryTickets(`SELECT ` + ticketCols + ` FROM tickets WHERE status NOT IN ('done','failed','abandoned')`)
}

// --- worktrees (one set per ticket: a tree per project repo) ---

type Worktree struct {
	ID        string
	TicketID  string
	RepoID    string
	RepoPath  string
	Path      string
	Branch    string
	CreatedAt int64
}

func (s *Store) AddWorktree(w Worktree) error {
	if w.ID == "" {
		w.ID = NewID("wt")
	}
	w.CreatedAt = now()
	_, err := s.db.Exec(`INSERT INTO worktrees (id, ticket_id, repo_id, repo_path, path, branch, created_at) VALUES (?,?,?,?,?,?,?)`,
		w.ID, w.TicketID, w.RepoID, w.RepoPath, w.Path, w.Branch, w.CreatedAt)
	return err
}

func (s *Store) WorktreesForTicket(ticketID string) ([]Worktree, error) {
	rows, err := s.db.Query(`SELECT id, ticket_id, repo_id, repo_path, path, branch, created_at FROM worktrees WHERE ticket_id = ? ORDER BY created_at, id`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Worktree
	for rows.Next() {
		var w Worktree
		if err := rows.Scan(&w.ID, &w.TicketID, &w.RepoID, &w.RepoPath, &w.Path, &w.Branch, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWorktree(id string) error {
	_, err := s.db.Exec(`DELETE FROM worktrees WHERE id = ?`, id)
	return err
}

// TicketsWithWorktrees returns ids of tickets that still hold worktree rows.
func (s *Store) TicketsWithWorktrees() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT ticket_id FROM worktrees`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DoneTicketsWithWorktree returns done tickets whose worktree lease was never
// returned (teardown interrupted, e.g. daemon restart).
func (s *Store) DoneTicketsWithWorktree() ([]Ticket, error) {
	return s.queryTickets(`SELECT ` + ticketCols + ` FROM tickets WHERE status = 'done' AND (worktree_path != '' OR id IN (SELECT ticket_id FROM worktrees))`)
}

// --- messages ---

type Message struct {
	ID        int64
	ProjectID string
	TicketID  string // "" for the project's main scroll
	Author    string // boss | pm | em | eng:<ticket-id> | system
	Kind      string // text | report | gate | system
	Body      string
	CreatedAt int64
}

func (s *Store) AppendMessage(m Message) (int64, error) {
	m.CreatedAt = now()
	if m.Kind == "" {
		m.Kind = "text"
	}
	res, err := s.db.Exec(`INSERT INTO messages (project_id, ticket_id, author, kind, body, created_at) VALUES (?,?,?,?,?,?)`,
		m.ProjectID, m.TicketID, m.Author, m.Kind, m.Body, m.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Messages returns the scroll for a project (ticketID == "") or ticket
// thread, oldest first.
func (s *Store) Messages(projectID, ticketID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT id, project_id, ticket_id, author, kind, body, created_at FROM (
			SELECT * FROM messages WHERE project_id = ? AND ticket_id = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC`, projectID, ticketID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.TicketID, &m.Author, &m.Kind, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- reads / unread badges ---

func scopeID(projectID, ticketID string) string {
	if ticketID == "" {
		return projectID
	}
	return projectID + ":" + ticketID
}

func (s *Store) MarkRead(projectID, ticketID string, lastMessageID int64) error {
	_, err := s.db.Exec(`INSERT INTO reads (scope_id, last_read_message_id) VALUES (?,?)
		ON CONFLICT(scope_id) DO UPDATE SET last_read_message_id = MAX(last_read_message_id, excluded.last_read_message_id)`,
		scopeID(projectID, ticketID), lastMessageID)
	return err
}

// UnreadCount counts messages after the read cursor, excluding the boss's own.
func (s *Store) UnreadCount(projectID, ticketID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages
		WHERE project_id = ? AND ticket_id = ? AND author != 'boss'
		AND id > COALESCE((SELECT last_read_message_id FROM reads WHERE scope_id = ?), 0)`,
		projectID, ticketID, scopeID(projectID, ticketID)).Scan(&n)
	return n, err
}

// --- attention items (My Office) ---

type ItemKind string

const (
	ItemQuestion ItemKind = "question"
	ItemOptions  ItemKind = "options"
	ItemDemo     ItemKind = "demo"
	ItemPlan     ItemKind = "plan"
	ItemBlocked  ItemKind = "blocked"
	ItemFailed   ItemKind = "failed"
	ItemHandbook ItemKind = "handbook"
)

type ItemTier string

const (
	TierInterrupt ItemTier = "interrupt"
	TierBreak     ItemTier = "break"
)

type Item struct {
	ID         string
	Kind       ItemKind
	Tier       ItemTier
	ProjectID  string
	TicketID   string
	RefID      string // plan id / handbook proposal path, when applicable
	Title      string
	Body       string
	Options    string // JSON array of {label, detail} for kind=options
	CreatedAt  int64
	ResolvedAt int64
}

const itemCols = `id, kind, tier, project_id, ticket_id, ref_id, title, body, options, created_at, resolved_at`

func (s *Store) CreateItem(it Item) (Item, error) {
	it.CreatedAt = now()
	if it.ID == "" {
		it.ID = NewID("itm")
	}
	_, err := s.db.Exec(`INSERT INTO items (`+itemCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,0)`,
		it.ID, it.Kind, it.Tier, it.ProjectID, it.TicketID, it.RefID, it.Title, it.Body, it.Options, it.CreatedAt)
	return it, err
}

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var it Item
	err := row.Scan(&it.ID, &it.Kind, &it.Tier, &it.ProjectID, &it.TicketID, &it.RefID, &it.Title, &it.Body, &it.Options, &it.CreatedAt, &it.ResolvedAt)
	return it, err
}

// OpenItems returns unresolved items, interrupts first, oldest first within
// a tier.
func (s *Store) OpenItems() ([]Item, error) {
	rows, err := s.db.Query(`SELECT ` + itemCols + ` FROM items WHERE resolved_at = 0
		ORDER BY CASE tier WHEN 'interrupt' THEN 0 ELSE 1 END, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) ItemByID(id string) (Item, error) {
	it, err := scanItem(s.db.QueryRow(`SELECT `+itemCols+` FROM items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return it, ErrNotFound
	}
	return it, err
}

func (s *Store) ResolveItem(id string) error {
	_, err := s.db.Exec(`UPDATE items SET resolved_at = ? WHERE id = ? AND resolved_at = 0`, now(), id)
	return err
}

// ResolveTicketItems resolves all open items attached to a ticket (called
// when the ticket leaves its waiting state).
func (s *Store) ResolveTicketItems(ticketID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM items WHERE ticket_id = ? AND resolved_at = 0`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := s.ResolveItem(id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// --- plans ---

type PlanStatus string

const (
	PlanPending  PlanStatus = "pending"
	PlanApproved PlanStatus = "approved"
	PlanRejected PlanStatus = "rejected"
)

type Plan struct {
	ID        string
	ProjectID string
	DocPath   string
	DocMD     string
	Tickets   string // JSON array of proposed tickets
	Questions string // JSON array of open questions
	Status    PlanStatus
	CreatedAt int64
}

const planCols = `id, project_id, doc_path, doc_md, tickets, questions, status, created_at`

func (s *Store) CreatePlan(p Plan) (Plan, error) {
	p.CreatedAt = now()
	if p.ID == "" {
		p.ID = NewID("plan")
	}
	if p.Status == "" {
		p.Status = PlanPending
	}
	_, err := s.db.Exec(`INSERT INTO plans (`+planCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		p.ID, p.ProjectID, p.DocPath, p.DocMD, p.Tickets, p.Questions, p.Status, p.CreatedAt)
	return p, err
}

func (s *Store) PlanByID(id string) (Plan, error) {
	var p Plan
	err := s.db.QueryRow(`SELECT `+planCols+` FROM plans WHERE id = ?`, id).
		Scan(&p.ID, &p.ProjectID, &p.DocPath, &p.DocMD, &p.Tickets, &p.Questions, &p.Status, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) SetPlanStatus(id string, status PlanStatus) error {
	_, err := s.db.Exec(`UPDATE plans SET status = ? WHERE id = ?`, status, id)
	return err
}

// --- settings (presence etc.) ---

func (s *Store) GetSetting(key, fallback string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// NewID returns a short random identifier with the given prefix, e.g. "prj_ab12cd34".
func NewID(prefix string) string {
	return fmt.Sprintf("%s_%08x", prefix, randUint32())
}
