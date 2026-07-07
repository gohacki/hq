// Package tui is hq's terminal UI. The home screen is My Office — a decision
// queue holding only what needs the boss. Everything else (projects, ticket
// threads, the board) is drill-down. A thin client: all state lives in the
// daemon and arrives over RPC + pushed events.
package tui

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"

	"github.com/gohacki/hq/internal/daemon"
	"github.com/gohacki/hq/internal/rpc"
	"github.com/gohacki/hq/internal/store"
)

// Run drives the TUI. redial (optional) reconnects to the daemon when the
// socket drops — e.g. across an `hq daemon restart` — so the TUI survives
// daemon deploys instead of dying with the connection.
func Run(cl *rpc.Client, redial func() (*rpc.Client, error)) error {
	if err := cl.Subscribe(); err != nil {
		return err
	}
	m := newModel(cl)
	m.redial = redial
	p := tea.NewProgram(m, tea.WithAltScreen())
	m.sendMsg = p.Send // lets background pumps (e.g. a live agent's pty) push repaints
	go pumpEvents(cl, p.Send)
	_, err := p.Run()
	return err
}

// pumpEvents forwards daemon events into the tea loop until the connection
// dies, then reports it. Restarted with a fresh client after a reconnect.
func pumpEvents(cl *rpc.Client, send func(tea.Msg)) {
	for ev := range cl.Events() {
		send(daemonEvent{ev})
	}
	send(daemonGone{})
}

// --- tea messages ---

type daemonEvent struct{ ev rpc.Event }
type daemonGone struct{}
type daemonBack struct{ cl *rpc.Client }
type daemonDead struct{ err error }
type refreshed struct {
	projects []daemon.ProjectView
	tickets  []daemon.TicketView // open project's tickets
	all      []daemon.TicketView // all tickets (board)
	messages []store.Message
	items    []store.Item
	presence string
}
type planLoaded struct{ plan store.Plan }
type docLoaded struct{ title, body string }
type errMsg struct{ err error }
type statusMsg struct{ s string }

// attachOpened says a live session was checked out and its tmux window is
// up. attachClosed (pushed by watchAttachPane via model.sendMsg) says the
// CLI pane died — the human exited the session or killed the window.
type attachOpened struct {
	winID, paneID       string
	projectID, ticketID string
}
type attachClosed struct{ paneID string }

// --- screens & focus ---

type screen int

const (
	screenBoard screen = iota // home: the kanban
	screenChat
	screenPlan
	screenDoc // read-only pager (the project playbook)
)

type focusArea int

const (
	focusSidebar focusArea = iota
	focusComposer
	focusMain // office cards / plan rows / board cards
)

type model struct {
	cl      *rpc.Client
	redial  func() (*rpc.Client, error) // reconnect after the socket drops (nil = die)
	sendMsg func(tea.Msg)               // == tea.Program.Send; lets pump goroutines push repaints

	projects []daemon.ProjectView
	tickets  []daemon.TicketView // of the open project
	all      []daemon.TicketView // department-wide (board)
	messages []store.Message
	items    []store.Item // open office items
	presence string

	screen     screen
	focus      focusArea
	showHelp   bool
	expandChat bool // ticket thread: x = expand collapsed engineer chatter

	// live agent session: checked out into a new tmux window (header pane +
	// the real harness CLI). Exactly one is ever attached.
	attachWin     string // attached window id ("" if none)
	attachPane    string // its CLI pane id (watched for exit)
	attachProject string
	attachTicket  string

	// attach picker overlay
	pickerOpen   bool
	picker       []pickerItem
	pickerCursor int

	// sidebar
	side       []sideItem
	sideCursor int

	// live agent activity: the in-progress turn (streaming text or a tool
	// line) for the open chat. Ephemeral — replaced by the real message row.
	stream rpc.StreamUpdate

	// markdown rendering (pi-style: real renderer + per-message cache)
	md      *glamour.TermRenderer
	mdWidth int
	mdCache map[int64]mdCache

	// board scope
	boardProject string // project id ("" = all projects)

	// doc pager (screenDoc)
	docTitle string
	docBody  string

	// chat scope
	openProject string // project id
	openTicket  string // ticket id ("" = project scroll)

	// plan review
	plan        store.Plan
	planTickets []planTicketRow
	planQs      []planQuestionRow
	planCursor  int
	planAnswer  int // index of question being answered in composer (-1 none)

	// board
	boardSel boardSel

	vp        viewport.Model
	composer  textarea.Model
	width     int
	height    int
	mainWidth int
	innerH    int
	status    string
	ready     bool

	// vim layer
	pendingG    bool   // a "g" was pressed — the next "g" jumps to the top
	cmdMode     byte   // 0 none · ':' command-line · '/' search (see vim.go)
	cmdBuf      string // text typed after : or /
	searchQuery string // last accepted / pattern; n/N reuse it

	bootstrapped bool // set on the first refresh; decides the landing screen once

	confirmDeleteProjectID string // armed by "D" on a project row; confirmed by "D" again
}

type planTicketRow struct {
	t        proposedTicket
	accepted bool
}

type planQuestionRow struct {
	q      string
	answer string
}

type proposedTicket struct {
	Repo  string `json:"repo"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Brief string `json:"brief"`
}

func newModel(cl *rpc.Client) *model {
	ta := textarea.New()
	ta.Placeholder = "Message… (enter to send, shift+enter for newline)"
	ta.SetHeight(3)
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	// focusSidebar so the row you're on is visible from the first frame —
	// otherwise the cursor is there but unrendered until you tab to it.
	m := &model{cl: cl, screen: screenBoard, focus: focusSidebar, presence: "available", planAnswer: -1}
	m.composer = ta
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), textarea.Blink)
}

// refresh re-pulls everything the current view needs. Cheap: local socket.
func (m *model) refresh() tea.Cmd {
	openProject, openTicket := m.openProject, m.openTicket
	if openProject == "" {
		openProject = m.boardProject // sidebar nests tickets under the board's scope too
	}
	return func() tea.Msg {
		var r refreshed
		if err := m.cl.Call("projects.list", nil, &r.projects); err != nil {
			return errMsg{err}
		}
		if err := m.cl.Call("items.list", nil, &r.items); err != nil {
			return errMsg{err}
		}
		if err := m.cl.Call("tickets.list", map[string]any{"project_id": ""}, &r.all); err != nil {
			return errMsg{err}
		}
		if err := m.cl.Call("presence.get", nil, &r.presence); err != nil {
			return errMsg{err}
		}
		if openProject != "" {
			if err := m.cl.Call("tickets.list", map[string]any{"project_id": openProject}, &r.tickets); err != nil {
				return errMsg{err}
			}
			if err := m.cl.Call("messages.list", map[string]any{
				"project_id": openProject, "ticket_id": openTicket,
			}, &r.messages); err != nil {
				return errMsg{err}
			}
		}
		return r
	}
}

// hasProjects reports whether the boss has briefed anything beyond the
// built-in director room yet.
func (m *model) hasProjects() bool {
	for _, p := range m.projects {
		if p.Name != store.DirectorRoomName {
			return true
		}
	}
	return false
}

func (m *model) directorRoomID() string {
	for _, p := range m.projects {
		if p.Name == store.DirectorRoomName {
			return p.ID
		}
	}
	return ""
}

func (m *model) markRead() tea.Cmd {
	if m.screen != screenChat || len(m.messages) == 0 {
		return nil
	}
	last := m.messages[len(m.messages)-1].ID
	p, t := m.openProject, m.openTicket
	return func() tea.Msg {
		m.cl.Call("reads.mark", map[string]any{"project_id": p, "ticket_id": t, "last_id": last}, nil)
		return nil
	}
}

func (m *model) send(body string) tea.Cmd {
	p, t := m.openProject, m.openTicket
	return func() tea.Msg {
		if err := m.cl.Call("message.send", map[string]any{
			"project_id": p, "ticket_id": t, "body": body,
		}, nil); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (m *model) call(method string, params map[string]any, okStatus string) tea.Cmd {
	return func() tea.Msg {
		var out json.RawMessage
		if err := m.cl.Call(method, params, &out); err != nil {
			return errMsg{err}
		}
		if okStatus != "" {
			return statusMsg{okStatus}
		}
		return statusMsg{strings.Trim(string(out), `"`)}
	}
}

// --- update ---

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.ready = true
		return m, nil

	case daemonGone:
		if m.redial == nil {
			m.status = "daemon connection lost — restart hq"
			return m, nil
		}
		m.status = "daemon connection lost — reconnecting…"
		redial := m.redial
		return m, func() tea.Msg {
			cl, err := redial()
			if err != nil {
				return daemonDead{err}
			}
			if err := cl.Subscribe(); err != nil {
				cl.Close()
				return daemonDead{err}
			}
			return daemonBack{cl}
		}

	case daemonBack:
		m.cl = msg.cl
		go pumpEvents(m.cl, m.sendMsg)
		m.status = "reconnected to the daemon"
		return m, m.refresh()

	case daemonDead:
		m.status = "daemon unreachable (" + msg.err.Error() + ") — fix it and restart hq"
		return m, nil

	case errMsg:
		m.status = msg.err.Error()
		return m, nil

	case statusMsg:
		m.status = msg.s
		return m, m.refresh()

	case refreshed:
		m.projects = msg.projects
		m.tickets = msg.tickets
		m.all = msg.all
		m.messages = msg.messages
		m.items = msg.items
		m.presence = msg.presence
		m.rebuildSidebar()
		if m.screen == screenBoard {
			m.normalizeBoardSel()
		}
		if !m.bootstrapped {
			m.bootstrapped = true
			if len(m.items) == 0 && !m.hasProjects() {
				// Nothing needs you and there's no project yet to brief —
				// land straight in the hq home chat instead of an empty board.
				if cr := m.directorRoomID(); cr != "" {
					return m, tea.Batch(m.openChat(cr, ""), m.markRead())
				}
			}
			// Otherwise: the board, with the sidebar cursor on the first
			// inbox item — the nav rows sit above it, so aim explicitly.
			for i, it := range m.side {
				if it.kind == rowItem {
					m.sideCursor = i
					break
				}
			}
		}
		m.renderMain()
		return m, m.markRead()

	case attachOpened:
		m.attachWin = msg.winID
		m.attachPane = msg.paneID
		m.attachProject = msg.projectID
		m.attachTicket = msg.ticketID
		m.status = "live session open in its own tmux window — close it (or exit the CLI) to hand back"
		paneID := msg.paneID
		go watchAttachPane(paneID, func() { m.sendMsg(attachClosed{paneID}) })
		return m, nil

	case attachClosed:
		if msg.paneID != m.attachPane {
			return m, nil // stale — already closed/replaced from hq's side
		}
		winID := m.attachWin
		projectID, ticketID := m.attachProject, m.attachTicket
		m.attachWin, m.attachPane, m.attachProject, m.attachTicket = "", "", "", ""
		m.status = "session handed back — headless supervision resumes on your next message"
		return m, tea.Batch(m.refresh(), func() tea.Msg {
			killWindow(winID) // the CLI pane died; take the header pane's window with it
			params := map[string]any{}
			if ticketID != "" {
				params["ticket_id"] = ticketID
			} else {
				params["project_id"] = projectID
			}
			m.cl.Call("session.checkin", params, nil)
			return nil
		})

	case planLoaded:
		return m, m.openPlanScreen(msg.plan)

	case docLoaded:
		m.docTitle = msg.title
		m.docBody = msg.body
		m.screen = screenDoc
		m.focus = focusMain
		m.renderMain()
		return m, nil

	case daemonEvent:
		return m.handleDaemonEvent(msg.ev)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	if m.focus == focusComposer {
		m.composer, cmd = m.composer.Update(msg)
	} else {
		m.vp, cmd = m.vp.Update(msg)
	}
	return m, cmd
}

func (m *model) handleDaemonEvent(ev rpc.Event) (tea.Model, tea.Cmd) {
	switch ev.Event {
	case rpc.EvAgentStream:
		// Local repaint only — streaming arrives at token rate and must
		// never fan out into refresh RPCs.
		var su rpc.StreamUpdate
		if json.Unmarshal(ev.Data, &su) == nil && m.screen == screenChat &&
			su.ProjectID == m.openProject && su.TicketID == m.openTicket {
			atBottom := m.vp.AtBottom()
			m.stream = su
			m.renderMain()
			if atBottom {
				m.vp.GotoBottom()
			}
		}
		return m, nil
	case rpc.EvMessageNew:
		var msg store.Message
		if json.Unmarshal(ev.Data, &msg) == nil && m.screen == screenChat &&
			msg.ProjectID == m.openProject && msg.TicketID == m.openTicket {
			m.stream = rpc.StreamUpdate{} // the real row replaces the live bubble
			m.messages = append(m.messages, msg)
			m.renderMain()
			m.vp.GotoBottom()
			return m, tea.Batch(m.markRead(), m.refresh())
		}
		return m, m.refresh()
	case rpc.EvItemNew:
		var it store.Item
		if json.Unmarshal(ev.Data, &it) == nil {
			m.status = fmt.Sprintf("🔔 %s: %s", it.Kind, it.Title)
			return m, tea.Batch(m.refresh(), m.notifyCmd(it))
		}
		return m, m.refresh()
	default:
		return m, m.refresh()
	}
}

// notifyCmd raises a desktop notification for a new office item, gated by
// presence: heads-down → interrupts only; available/review → everything.
func (m *model) notifyCmd(it store.Item) tea.Cmd {
	if m.presence == "heads-down" && it.Tier != store.TierInterrupt {
		return nil
	}
	return func() tea.Msg {
		if runtime.GOOS == "darwin" {
			script := fmt.Sprintf(`display notification %q with title "hq" subtitle %q`, it.Title, string(it.Kind))
			exec.Command("osascript", "-e", script).Run()
		}
		return nil
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	if n < 4 {
		n = 4
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
