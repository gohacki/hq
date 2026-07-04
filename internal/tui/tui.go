// Package tui is shipyard's Slack-like terminal UI: channel sidebar with
// unread badges, message scroll, task threads, composer. A thin client — all
// state lives in the daemon and arrives over RPC + pushed events.
package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gohacki/shipyard/internal/daemon"
	"github.com/gohacki/shipyard/internal/orch"
	"github.com/gohacki/shipyard/internal/rpc"
	"github.com/gohacki/shipyard/internal/store"
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

// --- messages ---

type daemonEvent struct{ ev rpc.Event }
type daemonGone struct{}
type refreshed struct {
	channels []daemon.ChannelView
	tasks    []daemon.TaskView
	messages []store.Message
}
type errMsg struct{ err error }

// --- model ---

type focusArea int

const (
	focusSidebar focusArea = iota
	focusComposer
)

// sideItem is one row in the sidebar: a channel, or a task thread nested
// under the open channel.
type sideItem struct {
	channel daemon.ChannelView
	task    *daemon.TaskView // nil for channel rows
}

type model struct {
	cl *rpc.Client

	channels []daemon.ChannelView
	tasks    []daemon.TaskView // tasks of the open channel
	messages []store.Message   // scroll of the open channel/thread

	items      []sideItem // rendered sidebar rows
	cursor     int        // sidebar cursor
	openChan   string     // channel id whose scroll is shown
	openThread string     // task id when a thread is open ("" = channel)

	focus    focusArea
	vp       viewport.Model
	composer textarea.Model
	width    int
	height   int
	status   string
	ready    bool
}

func newModel(cl *rpc.Client) *model {
	ta := textarea.New()
	ta.Placeholder = "Message… (enter to send, shift+enter for newline)"
	ta.SetHeight(3)
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	return &model{cl: cl, composer: ta, focus: focusComposer}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), textarea.Blink)
}

// refresh re-pulls everything the current view needs. Cheap: local socket.
func (m *model) refresh() tea.Cmd {
	openChan, openThread := m.openChan, m.openThread
	return func() tea.Msg {
		var chs []daemon.ChannelView
		if err := m.cl.Call("channels.list", nil, &chs); err != nil {
			return errMsg{err}
		}
		r := refreshed{channels: chs}
		if openChan == "" && len(chs) > 0 {
			openChan = chs[0].ID
		}
		if openChan != "" {
			if err := m.cl.Call("tasks.list", map[string]any{"channel_id": openChan}, &r.tasks); err != nil {
				return errMsg{err}
			}
			if err := m.cl.Call("messages.list", map[string]any{
				"channel_id": openChan, "task_id": openThread,
			}, &r.messages); err != nil {
				return errMsg{err}
			}
		}
		return r
	}
}

func (m *model) markRead() tea.Cmd {
	if len(m.messages) == 0 {
		return nil
	}
	last := m.messages[len(m.messages)-1].ID
	ch, th := m.openChan, m.openThread
	return func() tea.Msg {
		m.cl.Call("reads.mark", map[string]any{"channel_id": ch, "task_id": th, "last_id": last}, nil)
		return nil
	}
}

func (m *model) send(body string) tea.Cmd {
	ch, th := m.openChan, m.openThread
	return func() tea.Msg {
		if err := m.cl.Call("message.send", map[string]any{
			"channel_id": ch, "task_id": th, "body": body,
		}, nil); err != nil {
			return errMsg{err}
		}
		return nil
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
		m.status = "daemon connection lost — restart shipyard"
		return m, nil

	case errMsg:
		m.status = msg.err.Error()
		return m, nil

	case refreshed:
		m.channels = msg.channels
		m.tasks = msg.tasks
		m.messages = msg.messages
		if m.openChan == "" && len(m.channels) > 0 {
			m.openChan = m.channels[0].ID
		}
		m.rebuildSidebar()
		m.renderMessages()
		return m, m.markRead()

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
		if json.Unmarshal(ev.Data, &msg) == nil &&
			msg.ChannelID == m.openChan && msg.TaskID == m.openThread {
			m.messages = append(m.messages, msg)
			m.renderMessages()
			m.vp.GotoBottom()
			return m, tea.Batch(m.markRead(), m.refresh())
		}
		return m, m.refresh()
	case rpc.EvNeedsInput:
		var n rpc.NeedsInput
		if json.Unmarshal(ev.Data, &n) == nil {
			m.status = fmt.Sprintf("🔔 %s: %s", n.Reason, n.Summary)
			return m, tea.Batch(m.refresh(), notifyCmd(n))
		}
		return m, m.refresh()
	default:
		return m, m.refresh()
	}
}

// notifyCmd raises a desktop notification (macOS) for needs-input events.
func notifyCmd(n rpc.NeedsInput) tea.Cmd {
	return func() tea.Msg {
		if runtime.GOOS == "darwin" {
			script := fmt.Sprintf(`display notification %q with title "shipyard" subtitle %q`, n.Summary, n.Reason)
			exec.Command("osascript", "-e", script).Run()
		}
		return nil
	}
}

func (m *model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global keys.
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "tab":
		if m.focus == focusComposer {
			m.focus = focusSidebar
			m.composer.Blur()
		} else {
			m.focus = focusComposer
			return m, m.composer.Focus()
		}
		return m, nil
	}

	if m.focus == focusComposer {
		switch k.String() {
		case "enter":
			body := strings.TrimSpace(m.composer.Value())
			if body == "" {
				return m, nil
			}
			m.composer.Reset()
			return m, m.send(body)
		case "shift+enter", "alt+enter":
			m.composer.SetValue(m.composer.Value() + "\n")
			return m, nil
		case "esc":
			if m.openThread != "" {
				m.openThread = ""
				return m, m.refresh()
			}
			m.focus = focusSidebar
			m.composer.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.composer, cmd = m.composer.Update(k)
		return m, cmd
	}

	// Sidebar / navigation focus.
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
		return m, nil
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "enter":
		return m.openCursor()
	case "esc":
		if m.openThread != "" {
			m.openThread = ""
			return m, m.refresh()
		}
		return m, nil
	case "e":
		return m.openInstructions()
	case "t":
		return m.escapeHatch()
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return m.promoteProposal(int(k.String()[0] - '0'))
	case "g":
		m.vp.GotoTop()
		return m, nil
	case "G":
		m.vp.GotoBottom()
		return m, nil
	case "ctrl+d", "ctrl+u", "pgdown", "pgup":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return m, cmd
	}
	return m, nil
}

func (m *model) openCursor() (tea.Model, tea.Cmd) {
	if m.cursor >= len(m.items) {
		return m, nil
	}
	it := m.items[m.cursor]
	if it.task != nil {
		m.openThread = it.task.ID
	} else {
		m.openChan = it.channel.ID
		m.openThread = ""
	}
	m.focus = focusComposer
	return m, tea.Batch(m.refresh(), m.composer.Focus())
}

func (m *model) openInstructions() (tea.Model, tea.Cmd) {
	var res struct{ Path, Body string }
	if err := m.cl.Call("instructions.get", map[string]any{"channel_id": m.openChan}, &res); err != nil {
		m.status = err.Error()
		return m, nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, res.Path)
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return errMsg{err}
		}
		return nil
	})
}

func (m *model) escapeHatch() (tea.Model, tea.Cmd) {
	taskID := m.openThread
	if taskID == "" {
		if m.cursor < len(m.items) && m.items[m.cursor].task != nil {
			taskID = m.items[m.cursor].task.ID
		}
	}
	if taskID == "" {
		m.status = "open a task thread first (t = drop into crewmate session)"
		return m, nil
	}
	return m, func() tea.Msg {
		var win string
		if err := m.cl.Call("task.escape", map[string]any{"task_id": taskID}, &win); err != nil {
			return errMsg{err}
		}
		return errMsg{fmt.Errorf("opened tmux window %s", win)} // status line, not an error state
	}
}

// promoteProposal converts proposal N of the open scout thread's report into
// a ship task (scout→ship handoff).
func (m *model) promoteProposal(n int) (tea.Model, tea.Cmd) {
	if m.openThread == "" {
		return m, nil
	}
	var report string
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Kind == "report" {
			report = m.messages[i].Body
			break
		}
	}
	if report == "" {
		return m, nil
	}
	props := orch.ProposedTasks(report)
	if n < 1 || n > len(props) {
		m.status = fmt.Sprintf("report has %d proposed task(s)", len(props))
		return m, nil
	}
	taskID, proposal := m.openThread, props[n-1]
	return m, func() tea.Msg {
		var t store.Task
		if err := m.cl.Call("task.handoff", map[string]any{"task_id": taskID, "proposal": proposal}, &t); err != nil {
			return errMsg{err}
		}
		return errMsg{fmt.Errorf("ship task created: %s", t.Title)}
	}
}

// --- layout & rendering ---

const sidebarWidth = 28

var (
	styleSidebar     = lipgloss.NewStyle().Width(sidebarWidth).Padding(0, 1)
	styleSideSel     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62"))
	styleSideChan    = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleSideTask    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleBadge       = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("160")).Padding(0, 1).Bold(true)
	styleHeader      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("236")).Padding(0, 1)
	styleStatus      = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 1)
	styleAuthCaptain = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45"))
	styleAuthLead    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	styleAuthCrew    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	styleAuthSystem  = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Italic(true)
	styleTime        = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleReport      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(0, 1)
)

func (m *model) layout() {
	mainWidth := m.width - sidebarWidth - 1
	if mainWidth < 20 {
		mainWidth = 20
	}
	vpHeight := m.height - 1 /*header*/ - 5 /*composer*/ - 1 /*status*/
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.vp = viewport.New(mainWidth, vpHeight)
	m.composer.SetWidth(mainWidth - 2)
	m.renderMessages()
	m.vp.GotoBottom()
}

func (m *model) rebuildSidebar() {
	m.items = m.items[:0]
	for _, ch := range m.channels {
		m.items = append(m.items, sideItem{channel: ch})
		if ch.ID == m.openChan {
			for i := range m.tasks {
				m.items = append(m.items, sideItem{channel: ch, task: &m.tasks[i]})
			}
		}
	}
	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
}

func statusIcon(s store.TaskStatus) string {
	switch s {
	case store.TaskRunning:
		return "●"
	case store.TaskNeedsInput, store.TaskBlocked:
		return "✋"
	case store.TaskDelivering:
		return "🚀"
	case store.TaskAttached:
		return "⌨"
	case store.TaskDone:
		return "✓"
	case store.TaskFailed:
		return "✗"
	default:
		return "…"
	}
}

func (m *model) sidebarView(height int) string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("  shipyard") + "\n\n")
	for i, it := range m.items {
		var line string
		if it.task == nil {
			name := "# " + it.channel.Name
			badge := ""
			if it.channel.Unread > 0 {
				badge = " " + styleBadge.Render(fmt.Sprint(it.channel.Unread))
			}
			line = styleSideChan.Render(name) + badge
		} else {
			t := it.task
			name := fmt.Sprintf("  %s %s", statusIcon(t.Status), truncate(t.Title, sidebarWidth-8))
			badge := ""
			if t.Unread > 0 {
				badge = " " + styleBadge.Render(fmt.Sprint(t.Unread))
			}
			line = styleSideTask.Render(name) + badge
		}
		if i == m.cursor && m.focus == focusSidebar {
			line = styleSideSel.Render(stripANSIPad(line, sidebarWidth-2))
		}
		b.WriteString(line + "\n")
	}
	return styleSidebar.Height(height).Render(b.String())
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

// stripANSIPad renders selection rows at a fixed width (drop styling, pad).
func stripANSIPad(s string, w int) string {
	plain := stripANSI(s)
	if lipgloss.Width(plain) < w {
		plain += strings.Repeat(" ", w-lipgloss.Width(plain))
	}
	return plain
}

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == '\x1b':
			inEsc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func authorStyle(author string) (string, lipgloss.Style) {
	switch {
	case author == "captain":
		return "you", styleAuthCaptain
	case author == "lead":
		return "lead", styleAuthLead
	case strings.HasPrefix(author, "crew:"):
		return "crewmate", styleAuthCrew
	default:
		return "system", styleAuthSystem
	}
}

func (m *model) renderMessages() {
	if m.vp.Width == 0 {
		return
	}
	var b strings.Builder
	for _, msg := range m.messages {
		name, st := authorStyle(msg.Author)
		ts := time.Unix(msg.CreatedAt, 0).Format("15:04")
		b.WriteString(st.Render(name) + " " + styleTime.Render(ts) + "\n")
		body := msg.Body
		if msg.Kind == "report" {
			body = styleReport.Width(m.vp.Width - 4).Render(body)
		} else {
			body = lipgloss.NewStyle().Width(m.vp.Width - 2).Render(body)
		}
		b.WriteString(body + "\n\n")
	}
	if len(m.messages) == 0 {
		b.WriteString(styleAuthSystem.Render("no messages yet — say something below"))
	}
	m.vp.SetContent(b.String())
}

func (m *model) headerView() string {
	name := ""
	for _, ch := range m.channels {
		if ch.ID == m.openChan {
			name = "#" + ch.Name
			repos := make([]string, len(ch.Repos))
			for i, r := range ch.Repos {
				repos[i] = r.Name
			}
			if len(repos) > 0 {
				name += "  ·  " + strings.Join(repos, ", ") + "  ·  " + ch.Delivery
			}
		}
	}
	if m.openThread != "" {
		for _, t := range m.tasks {
			if t.ID == m.openThread {
				name += fmt.Sprintf("  ›  🧵 %s [%s]", t.Title, t.Status)
			}
		}
	}
	w := m.width - sidebarWidth - 1
	if w < 10 {
		w = 10
	}
	return styleHeader.Width(w).Render(name)
}

func (m *model) View() string {
	if !m.ready {
		return "loading…"
	}
	help := "tab: focus · enter: open/send · esc: back · e: instructions · t: tmux hatch · 1-9: promote proposal · q: quit"
	status := m.status
	if status == "" {
		status = help
	}
	main := lipgloss.JoinVertical(lipgloss.Left,
		m.headerView(),
		m.vp.View(),
		m.composer.View(),
		styleStatus.Render(truncate(status, m.width-sidebarWidth-2)),
	)
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		m.sidebarView(m.height),
		lipgloss.NewStyle().Height(m.height).Render("│"),
		main,
	)
	return body
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
