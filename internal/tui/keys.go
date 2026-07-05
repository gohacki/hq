package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gohacki/hq/internal/daemon"
)

func (m *model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Help overlay swallows keys until dismissed.
	if m.showHelp {
		switch k.String() {
		case "ctrl+d", "ctrl+u", "pgdown", "pgup", "j", "k", "down", "up", "g", "G":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(k)
			return m, cmd
		case "ctrl+c":
			return m, tea.Quit
		default:
			m.showHelp = false
			m.renderMain()
			return m, nil
		}
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

	// Non-composer global keys.
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.openHelp()
		return m, nil
	case "b":
		if m.screen == screenBoard {
			m.screen = screenOffice
		} else {
			m.screen = screenBoard
			m.focus = focusMain
		}
		m.renderMain()
		return m, nil
	case "M":
		return m.cyclePresence()
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

	if m.focus == focusSidebar {
		return m.sidebarKey(k)
	}

	switch m.screen {
	case screenOffice:
		return m.officeKey(k)
	case screenPlan:
		return m.planKey(k)
	case screenBoard:
		return m.boardKey(k)
	case screenChat:
		return m.chatKey(k)
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
	default:
		if m.screen == screenChat {
			m.focus = focusComposer
			return m, m.composer.Focus()
		}
		m.focus = focusSidebar
	}
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
		if m.openTicket != "" {
			m.openTicket = ""
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

func (m *model) chatKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "j", "down", "k", "up":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return m, cmd
	case "x":
		m.expandChat = !m.expandChat
		m.renderMain()
		return m, nil
	case "e":
		return m.openHandbook()
	case "v", "t":
		if m.openTicket != "" {
			return m, m.call("ticket.visit", map[string]any{"ticket_id": m.openTicket, "tmux_session": m.tmuxSession}, "")
		}
		return m, m.call("em.visit", map[string]any{"project_id": m.openProject, "tmux_session": m.tmuxSession}, "")
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return m.promoteProposal(int(k.String()[0] - '0'))
	case "esc":
		if m.openTicket != "" {
			m.openTicket = ""
			return m, m.refresh()
		}
		m.screen = screenOffice
		m.focus = focusMain
		m.renderMain()
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
	if m.openProject == "" {
		return m, nil
	}
	var res struct{ Path, Body string }
	if err := m.cl.Call("handbook.get", map[string]any{"project_id": m.openProject}, &res); err != nil {
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

// modelCommand implements the composer's /model command:
//
//	/model               show current models for this scope
//	/model opus          ticket open → that engineer; project → the EM
//	/model eng sonnet    project's default for future engineers
//	/model em fable      explicit EM switch
func (m *model) modelCommand(args []string) tea.Cmd {
	var cur daemon.ProjectView
	for _, p := range m.projects {
		if p.ID == m.openProject {
			cur = p
		}
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
	params := map[string]any{"project_id": m.openProject, "scope": scope, "model": modelName}
	if scope == "ticket" {
		params["ticket_id"] = m.openTicket
	}
	return m.call("model.set", params, "")
}

// openChat switches to a chat view for a project (and optional ticket).
func (m *model) openChat(projectID, ticketID string) tea.Cmd {
	m.screen = screenChat
	m.openProject = projectID
	m.openTicket = ticketID
	m.expandChat = false
	m.focus = focusComposer
	return tea.Batch(m.refresh(), m.composer.Focus())
}
