// Package tui is hq's terminal UI. The home screen is My Office — a decision
// queue holding only what needs the boss. Everything else (projects, ticket
// threads, the board) is drill-down. A thin client: all state lives in the
// daemon and arrives over RPC + pushed events.
package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gohacki/hq/internal/daemon"
	"github.com/gohacki/hq/internal/rpc"
	"github.com/gohacki/hq/internal/store"
)

func Run(cl *rpc.Client) error {
	if err := cl.Subscribe(); err != nil {
		return err
	}
	m := newModel(cl)
	p := tea.NewProgram(m, tea.WithAltScreen())
	go func() {
		for ev := range cl.Events() {
			p.Send(daemonEvent{ev})
		}
		p.Send(daemonGone{})
	}()
	_, err := p.Run()
	return err
}

// --- tea messages ---

type daemonEvent struct{ ev rpc.Event }
type daemonGone struct{}
type refreshed struct {
	projects []daemon.ProjectView
	tickets  []daemon.TicketView // open project's tickets
	all      []daemon.TicketView // all tickets (board)
	messages []store.Message
	items    []store.Item
	presence string
}
type planLoaded struct{ plan store.Plan }
type errMsg struct{ err error }
type statusMsg struct{ s string }

// --- screens & focus ---

type screen int

const (
	screenOffice screen = iota
	screenChat
	screenPlan
	screenBoard
)

type focusArea int

const (
	focusSidebar focusArea = iota
	focusComposer
	focusMain // office cards / plan rows / board cards
)

type model struct {
	cl *rpc.Client

	projects []daemon.ProjectView
	tickets  []daemon.TicketView // of the open project
	all      []daemon.TicketView // department-wide (board)
	messages []store.Message
	items    []store.Item // open office items
	presence string

	screen      screen
	focus       focusArea
	showHelp    bool
	expandChat  bool // ticket timeline: x = full chat
	tmuxSession string

	// sidebar
	side       []sideItem
	sideCursor int

	// chat scope
	openProject string // project id
	openTicket  string // ticket id ("" = project scroll)

	// office
	officeCursor int
	optionsMode  bool // selected options item awaits 1-9

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
	m := &model{cl: cl, screen: screenOffice, focus: focusMain, presence: "available", planAnswer: -1}
	if os.Getenv("TMUX") != "" {
		if out, err := exec.Command("tmux", "display-message", "-p", "#S").Output(); err == nil {
			m.tmuxSession = strings.TrimSpace(string(out))
		}
	}
	m.composer = ta
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), textarea.Blink)
}

// refresh re-pulls everything the current view needs. Cheap: local socket.
func (m *model) refresh() tea.Cmd {
	openProject, openTicket := m.openProject, m.openTicket
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
		m.status = "daemon connection lost — restart hq"
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
		if m.officeCursor >= len(m.items) {
			m.officeCursor = max(0, len(m.items)-1)
		}
		m.rebuildSidebar()
		m.renderMain()
		return m, m.markRead()

	case planLoaded:
		m.openPlanScreen(msg.plan)
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
	case rpc.EvMessageNew:
		var msg store.Message
		if json.Unmarshal(ev.Data, &msg) == nil && m.screen == screenChat &&
			msg.ProjectID == m.openProject && msg.TicketID == m.openTicket {
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
