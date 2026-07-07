package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gohacki/hq/internal/daemon"
	"github.com/gohacki/hq/internal/rpc"
	"github.com/gohacki/hq/internal/store"
)

func (m *model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A delete-project confirmation only stays armed for the very next
	// keypress — anything other than a second "D" disarms it, so it can
	// never fire later on an unrelated keystroke.
	if m.confirmDeleteProjectID != "" && k.String() != "D" {
		m.confirmDeleteProjectID = ""
	}

	// Help overlay swallows keys until dismissed.
	if m.showHelp {
		switch k.String() {
		case "ctrl+d", "ctrl+u", "pgdown", "pgup", "j", "k", "down", "up":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(k)
			return m, cmd
		case "g":
			m.vp.GotoTop()
			return m, nil
		case "G":
			m.vp.GotoBottom()
			return m, nil
		case "ctrl+f":
			m.vp.ViewDown()
			return m, nil
		case "ctrl+b":
			m.vp.ViewUp()
			return m, nil
		case "ctrl+c":
			return m, tea.Quit
		default:
			m.showHelp = false
			m.renderMain()
			return m, nil
		}
	}

	// A : command or / search being typed captures everything.
	if m.cmdMode != 0 {
		return m.cmdlineKey(k)
	}

	// The attach picker is modal.
	if m.pickerOpen {
		return m.pickerKey(k)
	}

	// Composer answering a plan question is modal.
	if m.screen == screenPlan && m.planAnswer >= 0 && m.focus == focusComposer {
		switch k.String() {
		case "enter":
			m.planQs[m.planAnswer].answer = strings.TrimSpace(m.composer.Value())
			m.composer.Reset()
			m.planAnswer = -1
			m.focus = focusMain
			m.composer.Blur()
			m.renderMain()
			return m, nil
		case "esc":
			m.composer.Reset()
			m.planAnswer = -1
			m.focus = focusMain
			m.composer.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.composer, cmd = m.composer.Update(k)
		return m, cmd
	}

	// Global keys.
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "tab":
		return m.cycleFocus()
	}

	if m.focus == focusComposer {
		return m.composerKey(k)
	}

	// gg chord: a bare g arms it; a second g jumps to the top. Any other key
	// falls through to its normal meaning (the arm is consumed silently).
	if m.pendingG {
		m.pendingG = false
		if k.String() == "g" {
			return m.gotoFirst()
		}
	} else if k.String() == "g" {
		m.pendingG = true
		return m, nil
	}

	// Non-composer global keys.
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.openHelp()
		return m, nil
	case ":", "/":
		m.cmdMode = k.String()[0]
		m.cmdBuf = ""
		return m, nil
	case "n":
		return m.searchMove(1)
	case "N":
		return m.searchMove(-1)
	case "b":
		if m.screen != screenBoard {
			m.focus = focusMain
			// From a project chat, b means that project's board; the
			// director chat and everywhere else fall back to the last scope.
			scope := m.boardProject
			if m.screen == screenChat && m.openProject != "" && m.openProject != m.directorRoomID() {
				scope = m.openProject
			}
			return m, m.gotoBoard(scope)
		}
		return m, nil
	case "M":
		return m.cyclePresence()
	case "G":
		return m.gotoLast()
	case "i":
		// Insert mode: chat is the only screen with a composer.
		if m.screen == screenChat {
			m.focus = focusComposer
			return m, m.composer.Focus()
		}
		return m, nil
	case "v", "t":
		pid, tid := m.attachTarget()
		if pid != "" {
			m.openPicker(pid, tid)
		}
		return m, nil
	case "p":
		if pid, _ := m.attachTarget(); pid != "" && m.projectName(pid) != "hq" {
			return m, m.openPlaybook(pid)
		}
		return m, nil
	case "ctrl+f":
		m.vp.ViewDown()
		return m, nil
	case "ctrl+b":
		m.vp.ViewUp()
		return m, nil
	case "ctrl+d", "ctrl+u", "pgdown", "pgup":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return m, cmd
	}

	if m.focus == focusSidebar {
		return m.sidebarKey(k)
	}

	switch m.screen {
	case screenPlan:
		return m.planKey(k)
	case screenBoard:
		return m.boardKey(k)
	case screenChat:
		return m.chatKey(k)
	case screenDoc:
		return m.docKey(k)
	}
	return m, nil
}

// openPlaybook fetches a project's playbook and opens the pager.
func (m *model) openPlaybook(projectID string) tea.Cmd {
	name := m.projectName(projectID)
	return func() tea.Msg {
		var res struct {
			Exists bool                       `json:"exists"`
			Prose  string                     `json:"prose"`
			Repos  map[string]json.RawMessage `json:"repos"`
		}
		if err := m.cl.Call("playbook.get", map[string]any{"project_id": projectID}, &res); err != nil {
			return errMsg{err}
		}
		body := strings.TrimSpace(res.Prose)
		if len(res.Repos) > 0 {
			var names []string
			for n := range res.Repos {
				names = append(names, n)
			}
			sort.Strings(names)
			var sb strings.Builder
			sb.WriteString("\n\n## Machine recipe (playbook.json)\n")
			for _, n := range names {
				pretty, _ := json.MarshalIndent(json.RawMessage(res.Repos[n]), "  ", "  ")
				fmt.Fprintf(&sb, "\n### %s\n\n  %s\n", n, string(pretty))
			}
			body += sb.String()
		}
		if strings.TrimSpace(body) == "" {
			body = "No playbook yet.\n\nOpen this project's chat — the EM runs a one-time setup interview\ncapturing the whole lifecycle (worktrees, dev servers, pipeline, the\ndelivery gate) into the playbook."
		}
		return docLoaded{title: "📖 playbook — " + name, body: body}
	}
}

func (m *model) docKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "j", "down", "k", "up":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return m, cmd
	case "h", "left":
		m.focus = focusSidebar
		return m, nil
	case "esc":
		return m.escBack()
	}
	return m, nil
}

// attachTarget picks the project/ticket the attach picker should open for,
// from wherever the cursor is.
func (m *model) attachTarget() (projectID, ticketID string) {
	if m.focus == focusSidebar && m.sideCursor < len(m.side) {
		it := m.side[m.sideCursor]
		switch it.kind {
		case rowItem:
			return it.item.ProjectID, it.item.TicketID
		case rowHome:
			return it.project.ID, ""
		case rowProject, rowEM:
			return it.project.ID, ""
		case rowTicket, rowEng:
			return it.project.ID, it.ticket.ID
		}
		return "", ""
	}
	switch m.screen {
	case screenChat:
		return m.openProject, m.openTicket
	case screenBoard:
		if pid, tid := m.selectedBoardTicket(); pid != "" {
			return pid, tid
		}
		if m.boardProject != "" {
			return m.boardProject, ""
		}
	case screenPlan:
		return m.plan.ProjectID, ""
	}
	return "", ""
}

// gotoBoard is the ONE way to land on the board: it also clears the chat
// scope, or the next refresh would fetch the old chat's tickets and the
// sidebar would nest them under whatever project the board now shows.
// Focus is left alone — callers decide (sidebar navigation keeps the
// sidebar cursor; b/plan-approve put you on the cards).
func (m *model) gotoBoard(projectID string) tea.Cmd {
	m.boardProject = projectID
	m.openProject, m.openTicket = "", ""
	m.screen = screenBoard
	m.composer.Blur()
	m.renderMain()
	return m.refresh()
}

// escBack is the one esc ladder: chat → project board → all-projects
// board, always ending with the cursor visible in the sidebar — esc never
// strands focus somewhere nothing is highlighted.
func (m *model) escBack() (tea.Model, tea.Cmd) {
	m.focus = focusSidebar
	switch m.screen {
	case screenChat:
		target := ""
		if m.openProject != "" && m.projectName(m.openProject) != "hq" {
			target = m.openProject
		}
		return m, m.gotoBoard(target)
	case screenPlan, screenDoc:
		return m, m.gotoBoard(m.boardProject)
	case screenBoard:
		if m.boardProject != "" {
			return m, m.gotoBoard("")
		}
	}
	return m, nil
}

func (m *model) cycleFocus() (tea.Model, tea.Cmd) {
	switch m.focus {
	case focusComposer:
		m.focus = focusSidebar
		m.composer.Blur()
	case focusSidebar:
		m.focus = focusMain
		if m.screen == screenChat {
			m.focus = focusComposer
			return m, m.composer.Focus()
		}
		if m.screen == screenBoard {
			m.normalizeBoardSel()
		}
	default:
		if m.screen == screenChat {
			m.focus = focusComposer
			return m, m.composer.Focus()
		}
		m.focus = focusSidebar
	}
	m.renderMain() // focus markers (board column ▸) only draw on re-render
	return m, nil
}

func (m *model) cyclePresence() (tea.Model, tea.Cmd) {
	next := map[string]string{"available": "heads-down", "heads-down": "review", "review": "available"}[m.presence]
	if next == "" {
		next = "available"
	}
	return m, m.call("presence.set", map[string]any{"mode": next}, "presence → "+next)
}

func (m *model) composerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "enter":
		body := strings.TrimSpace(m.composer.Value())
		if body == "" {
			return m, nil
		}
		m.composer.Reset()
		if strings.HasPrefix(body, "/model") {
			return m, m.modelCommand(strings.Fields(body)[1:])
		}
		if body == "/help" || body == "/?" {
			m.openHelp()
			return m, nil
		}
		return m, m.send(body)
	case "shift+enter", "alt+enter":
		m.composer.SetValue(m.composer.Value() + "\n")
		return m, nil
	case "esc":
		// Leave insert mode into the sidebar — the one place a cursor is
		// always visible. (Scrolling the thread doesn't need main focus:
		// ctrl+d/u/f/b and gg/G work from anywhere.)
		m.focus = focusSidebar
		m.composer.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(k)
	return m, cmd
}

func (m *model) chatKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	pending := m.pendingItem()
	switch k.String() {
	case "j", "down", "k", "up":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return m, cmd
	case "h", "left":
		m.focus = focusSidebar
		return m, nil
	case "x":
		m.expandChat = !m.expandChat
		m.renderMain()
		return m, nil
	case "e":
		return m.openHandbook()
	case "a":
		return m.approveItem(pending)
	case "r":
		return m.retryItem(pending)
	case "d":
		return m.dismissItem(pending)
	case "P":
		if pending != nil && pending.Kind == store.ItemPlan {
			return m.openItem(pending)
		}
		return m, nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		n := int(k.String()[0] - '0')
		if pending != nil && pending.Kind == store.ItemOptions {
			return m.pickOption(*pending, n)
		}
		return m.promoteProposal(n)
	case "esc":
		// Scroll mode → the sidebar first (same as the board's cards);
		// esc there backs out to the project board.
		m.focus = focusSidebar
		return m, nil
	case "enter":
		m.focus = focusComposer
		return m, m.composer.Focus()
	}
	return m, nil
}

// promoteProposal converts proposal N of the open spike thread's report into
// a build ticket (spike→build handoff).
func (m *model) promoteProposal(n int) (tea.Model, tea.Cmd) {
	if m.openTicket == "" {
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
	props := proposedFromReport(report)
	if n < 1 || n > len(props) {
		m.status = fmt.Sprintf("report has %d proposed ticket(s)", len(props))
		return m, nil
	}
	return m, m.call("ticket.handoff", map[string]any{"ticket_id": m.openTicket, "proposal": props[n-1]}, "build ticket opened: "+truncate(props[n-1], 60))
}

// proposedFromReport mirrors orch.ProposedTickets (kept local to avoid the
// import; the daemon re-validates on handoff).
func proposedFromReport(report string) []string {
	var out []string
	inSection := false
	for _, line := range strings.Split(report, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			h := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
			inSection = h == "proposed tickets" || h == "proposed tasks"
			continue
		}
		if inSection && strings.HasPrefix(trimmed, "- ") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		}
	}
	return out
}

func (m *model) openHandbook() (tea.Model, tea.Cmd) {
	pid := m.openProject
	if pid == "" {
		pid = m.boardProject
	}
	if pid == "" {
		return m, nil
	}
	var res struct{ Path, Body string }
	if err := m.cl.Call("handbook.get", map[string]any{"project_id": pid}, &res); err != nil {
		m.status = err.Error()
		return m, nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return m, tea.ExecProcess(exec.Command(editor, res.Path), func(err error) tea.Msg {
		if err != nil {
			return errMsg{err}
		}
		return statusMsg{"handbook saved"}
	})
}

// modelCommand implements /model (composer) and :model (command line):
//
//	/model               show current models for this scope
//	/model opus          ticket open → that engineer; project → the EM
//	/model eng sonnet    project's default for future engineers
//	/model em fable      explicit EM switch
func (m *model) modelCommand(args []string) tea.Cmd {
	var cur daemon.ProjectView
	pid := m.openProject
	if pid == "" {
		pid = m.boardProject
	}
	for _, p := range m.projects {
		if p.ID == pid {
			cur = p
		}
	}
	if cur.ID == "" {
		m.status = "open a project first — /model is scope-aware"
		return nil
	}
	orDefault := func(s string) string {
		if s == "" {
			return "sonnet (default)"
		}
		return s
	}
	if len(args) == 0 {
		if m.openTicket != "" {
			for _, t := range m.tickets {
				if t.ID == m.openTicket {
					m.status = fmt.Sprintf("engineer model: %s · /model <sonnet|opus|fable|haiku> to switch", orDefault(t.Model))
					return nil
				}
			}
		}
		m.status = fmt.Sprintf("manager: %s · engineer default: %s · /model <m> = manager, /model eng <m> = engineer default",
			orDefault(cur.EMModel), orDefault(cur.EngModel))
		return nil
	}
	scope, modelName := "", ""
	switch {
	case len(args) == 1:
		modelName = args[0]
		if m.openTicket != "" {
			scope = "ticket"
		} else {
			scope = "em"
		}
	case args[0] == "eng":
		scope, modelName = "eng", args[1]
	case args[0] == "em":
		scope, modelName = "em", args[1]
	default:
		m.status = "usage: /model [em|eng] <sonnet|opus|fable|haiku>"
		return nil
	}
	params := map[string]any{"project_id": cur.ID, "scope": scope, "model": modelName}
	if scope == "ticket" {
		params["ticket_id"] = m.openTicket
	}
	return m.call("model.set", params, "")
}

// openChat switches to a chat view for a project (and optional ticket): the
// hq-rendered thread, always. Live sessions are opened explicitly with v.
func (m *model) openChat(projectID, ticketID string) tea.Cmd {
	m.screen = screenChat
	m.openProject = projectID
	m.openTicket = ticketID
	m.expandChat = false
	m.stream = rpc.StreamUpdate{}
	m.focus = focusComposer
	return tea.Batch(m.refresh(), m.composer.Focus())
}
