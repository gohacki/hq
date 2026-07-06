package tui

// The attach picker: pressing v anywhere ticket- or project-scoped opens a
// small overlay listing the agents you could sit down with (the project's
// EM, and each live engineer). Picking one checks its session out of
// headless supervision and opens it in a new tmux window — the real
// interactive harness CLI. Closing that window (or the CLI exiting) checks
// the session back in.

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"
)

type pickerItem struct {
	label     string
	detail    string
	projectID string
	ticketID  string // "" = the EM
	ready     bool   // has a session to attach to
}

// openPicker builds the candidate list for a project (and optionally a
// ticket to put first) and shows the overlay.
func (m *model) openPicker(projectID, ticketID string) {
	if !tmuxAvailable() {
		m.status = "run hq inside tmux to open live sessions"
		return
	}
	var items []pickerItem
	var prj = m.findProject(projectID)
	addTicket := func(tid string) {
		for _, t := range m.tickets {
			if t.ID == tid && !t.Status.Terminal() {
				items = append(items, pickerItem{
					label:     "engineer — " + truncate(t.Title, 34),
					detail:    fmt.Sprintf("%s %s · %s", statusIcon(t.Status), t.Status, orDefaultModel(t.Model)),
					projectID: projectID, ticketID: t.ID,
					ready: t.SessionID != "" && t.WorktreePath != "",
				})
			}
		}
	}
	if ticketID != "" {
		addTicket(ticketID)
	}
	items = append(items, pickerItem{
		label:     "EM — " + prj.Name,
		detail:    "manager · " + orDefaultModel(prj.EMModel),
		projectID: projectID,
		ready:     prj.EMSessionID != "",
	})
	for _, t := range m.tickets {
		if t.ID == ticketID || t.ProjectID != projectID {
			continue
		}
		if t.SessionID != "" && !t.Status.Terminal() {
			addTicket(t.ID)
		}
	}
	m.picker = items
	m.pickerCursor = 0
	m.pickerOpen = true
}

func (m *model) findProject(id string) (p struct {
	Name        string
	EMModel     string
	EMSessionID string
}) {
	for _, pr := range m.projects {
		if pr.ID == id {
			p.Name, p.EMModel, p.EMSessionID = pr.Name, pr.EMModel, pr.EMSessionID
			if pr.Name == "conference-room" {
				p.Name = "hq"
			}
		}
	}
	return p
}

func orDefaultModel(s string) string {
	if s == "" {
		return "sonnet"
	}
	return s
}

func (m *model) pickerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "j", "down":
		if m.pickerCursor < len(m.picker)-1 {
			m.pickerCursor++
		}
		return m, nil
	case "k", "up":
		if m.pickerCursor > 0 {
			m.pickerCursor--
		}
		return m, nil
	case "enter", "v", "l":
		if m.pickerCursor < len(m.picker) {
			it := m.picker[m.pickerCursor]
			m.pickerOpen = false
			if !it.ready {
				m.status = "no session yet — message them first, then attach"
				return m, nil
			}
			return m, m.attachSession(it)
		}
		return m, nil
	case "esc", "q", "ctrl+c":
		m.pickerOpen = false
		return m, nil
	}
	return m, nil
}

var (
	stylePickerBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).Padding(1, 2)
	stylePickerSel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62"))
	stylePickerDim = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
)

// pickerView renders the overlay, centered in the main area.
func (m *model) pickerView() string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Open live session") + "\n")
	b.WriteString(stylePickerDim.Render("the real CLI, in a new tmux window — close it to hand back") + "\n\n")
	for i, it := range m.picker {
		line := "  " + it.label
		if !it.ready {
			line += stylePickerDim.Render("  (no session yet)")
		}
		line += "\n    " + stylePickerDim.Render(it.detail)
		if i == m.pickerCursor {
			line = stylePickerSel.Render(stripANSIPad("▸ "+it.label, 44)) + "\n    " + stylePickerDim.Render(it.detail)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + stylePickerDim.Render("j/k: move · enter: attach · esc: cancel"))
	return stylePickerBox.Render(b.String())
}

// attachSession checks the picked agent's session out and opens it in a new
// tmux window (header pane + CLI). Any previously attached window is closed
// (checking that session back in) first.
func (m *model) attachSession(it pickerItem) tea.Cmd {
	prev := m.closeAttachWindow()
	projectID, ticketID := it.projectID, it.ticketID
	name := "hq:" + m.projectName(projectID)
	if ticketID != "" {
		name = "hq:" + truncate(m.ticketTitle(ticketID), 20)
	}
	open := func() tea.Msg {
		var co struct {
			Argv []string `json:"argv"`
			Dir  string   `json:"dir"`
		}
		params := map[string]any{}
		if ticketID != "" {
			params["ticket_id"] = ticketID
		} else {
			params["project_id"] = projectID
		}
		if err := m.cl.Call("session.checkout", params, &co); err != nil {
			return errMsg{err}
		}
		headerArgv, err := attachHeaderArgv(projectID, ticketID)
		if err != nil {
			return errMsg{err}
		}
		winID, paneID, err := openAttachWindow(name, co.Dir, co.Argv, headerArgv)
		if err != nil {
			// Checkout succeeded but the window didn't open — hand the
			// session straight back so it isn't stranded.
			m.cl.Call("session.checkin", params, nil)
			return errMsg{err}
		}
		return attachOpened{winID, paneID, projectID, ticketID}
	}
	if prev == nil {
		return open
	}
	return tea.Sequence(prev, open)
}

// closeAttachWindow kills any open attached window and returns the tea.Cmd
// that checks its session back in. Safe to call with nothing attached.
func (m *model) closeAttachWindow() tea.Cmd {
	if m.attachWin == "" {
		return nil
	}
	winID := m.attachWin
	projectID, ticketID := m.attachProject, m.attachTicket
	m.attachWin, m.attachPane, m.attachProject, m.attachTicket = "", "", "", ""
	return func() tea.Msg {
		killWindow(winID)
		params := map[string]any{}
		if ticketID != "" {
			params["ticket_id"] = ticketID
		} else {
			params["project_id"] = projectID
		}
		m.cl.Call("session.checkin", params, nil)
		return nil
	}
}

// attachHeaderArgv builds the argv for the info pane above an attached CLI:
// hq itself, re-invoked as `hq chat-header`.
func attachHeaderArgv(projectID, ticketID string) ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	argv := []string{exe, "chat-header", "--project", projectID}
	if ticketID != "" {
		argv = append(argv, "--ticket", ticketID)
	}
	return argv, nil
}
