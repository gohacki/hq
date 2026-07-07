package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gohacki/hq/internal/daemon"
	"github.com/gohacki/hq/internal/store"
)

// The sidebar is hq's constant left rail:
//
//	global nav    — the all-projects board and the ◆ hq director chat,
//	                pinned at the very top: destinations, not projects
//	⚠ needs you   — the inbox: every open attention item, interrupts first
//	projects      — each project (tickets nest under the focused one)
//	staff         — the focused project's EM and live engineers
//
// The kanban board is the home screen; the inbox sits right under the two
// nav rows because "what needs me right now" is the first question the
// screen must answer.

type rowKind int

const (
	rowItem rowKind = iota // inbox: an open attention item
	rowHeader
	rowAll     // board, all projects
	rowHome    // the hq home chat (director)
	rowProject // enter = that project's EM chat (b = its board)
	rowTicket  // ticket thread nested under the focused project
	rowEM      // staff: the focused project's manager
	rowEng     // staff: a live engineer
)

type sideItem struct {
	kind    rowKind
	label   string
	project daemon.ProjectView
	ticket  *daemon.TicketView
	item    *store.Item
}

func (it sideItem) selectable() bool { return it.kind != rowHeader }

// focusedProjectID is the project the sidebar nests tickets/staff under:
// the board's scope, or the open chat's project.
func (m *model) focusedProjectID() string {
	if m.screen == screenChat {
		return m.openProject
	}
	return m.boardProject
}

// isCurrentRow reports whether it is the screen you're actually looking at
// right now, independent of whether the sidebar itself has keyboard focus.
func (m *model) isCurrentRow(it sideItem) bool {
	switch it.kind {
	case rowAll:
		return m.screen == screenBoard && m.boardProject == ""
	case rowHome:
		return m.screen == screenChat && m.openProject == it.project.ID
	case rowProject:
		if m.screen == screenBoard && m.boardProject == it.project.ID {
			return true
		}
		return m.screen == screenChat && m.openProject == it.project.ID && m.openTicket == ""
	case rowTicket:
		return m.screen == screenChat && it.ticket != nil && m.openTicket == it.ticket.ID
	default:
		return false
	}
}

func (m *model) rebuildSidebar() {
	m.side = m.side[:0]

	// Global nav first: the board and the director chat are destinations,
	// not projects — they sit above the inbox and the project list.
	var home daemon.ProjectView
	for _, p := range m.projects {
		if p.Name == store.DirectorRoomName {
			home = p
		}
	}
	m.side = append(m.side, sideItem{kind: rowAll})
	m.side = append(m.side, sideItem{kind: rowHome, project: home})

	if len(m.items) > 0 {
		m.side = append(m.side, sideItem{kind: rowHeader, label: fmt.Sprintf("needs you (%d)", len(m.items))})
		for i := range m.items {
			m.side = append(m.side, sideItem{kind: rowItem, item: &m.items[i]})
		}
	}

	focused := m.focusedProjectID()
	var open daemon.ProjectView
	hasProjects := false
	for _, p := range m.projects {
		if p.Name == store.DirectorRoomName {
			continue
		}
		if !hasProjects {
			hasProjects = true
			m.side = append(m.side, sideItem{kind: rowHeader, label: "projects"})
		}
		if p.ID == focused {
			open = p
		}
		m.side = append(m.side, sideItem{kind: rowProject, project: p})
		if p.ID == focused {
			for i := range m.tickets {
				if m.tickets[i].Status.Terminal() && m.tickets[i].Unread == 0 {
					continue
				}
				m.side = append(m.side, sideItem{kind: rowTicket, project: p, ticket: &m.tickets[i]})
			}
		}
	}

	if open.ID != "" {
		m.side = append(m.side, sideItem{kind: rowHeader, label: "staff"})
		m.side = append(m.side, sideItem{kind: rowEM, project: open})
		for i := range m.tickets {
			t := &m.tickets[i]
			if t.SessionID != "" && !t.Status.Terminal() {
				m.side = append(m.side, sideItem{kind: rowEng, project: open, ticket: t})
			}
		}
	}
	if m.sideCursor >= len(m.side) {
		m.sideCursor = max(0, len(m.side)-1)
	}
	m.skipHeader(1)
}

func (m *model) skipHeader(dir int) {
	for m.sideCursor >= 0 && m.sideCursor < len(m.side) && !m.side[m.sideCursor].selectable() {
		next := m.sideCursor + dir
		if next < 0 || next >= len(m.side) {
			dir = -dir
			next = m.sideCursor + dir
			if next < 0 || next >= len(m.side) {
				return
			}
		}
		m.sideCursor = next
	}
}

func (m *model) sidebarKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cur *sideItem
	if m.sideCursor < len(m.side) {
		cur = &m.side[m.sideCursor]
	}
	switch k.String() {
	case "j", "down":
		if m.sideCursor < len(m.side)-1 {
			m.sideCursor++
			m.skipHeader(1)
		}
		return m, nil
	case "k", "up":
		if m.sideCursor > 0 {
			m.sideCursor--
			m.skipHeader(-1)
		}
		return m, nil
	case "enter":
		return m.openSideCursor()
	case "l", "right":
		// Back into the main area (mirrors h from the main area's edge).
		// In a chat that's thread-scroll mode — i or enter reach the composer.
		m.focus = focusMain
		if m.screen == screenBoard {
			m.normalizeBoardSel() // land on a card, never an empty column
		}
		m.renderMain() // the column marker only draws on re-render
		return m, nil
	case "e":
		return m.openHandbook()
	case "a":
		if cur != nil && cur.kind == rowItem {
			return m.approveItem(cur.item)
		}
		return m, nil
	case "r":
		if cur != nil && cur.kind == rowItem {
			return m.retryItem(cur.item)
		}
		return m, nil
	case "x", "d":
		if cur != nil && cur.kind == rowItem {
			return m.dismissItem(cur.item)
		}
		return m, nil
	case "esc":
		return m.escBack()
	case "D":
		return m.deleteProjectKey()
	}
	return m, nil
}

func (m *model) openSideCursor() (tea.Model, tea.Cmd) {
	if m.sideCursor >= len(m.side) {
		return m, nil
	}
	it := m.side[m.sideCursor]
	switch it.kind {
	case rowItem:
		return m.openItem(it.item)
	case rowAll:
		return m, m.gotoBoard("")
	case rowHome:
		return m, m.openChat(it.project.ID, "")
	case rowProject:
		// A project opens as a conversation with its EM; the scoped board
		// stays one keystroke away (b).
		return m, m.openChat(it.project.ID, "")
	case rowTicket, rowEng:
		return m, m.openChat(it.project.ID, it.ticket.ID)
	case rowEM:
		return m, m.openChat(it.project.ID, "")
	}
	return m, nil
}

// deleteProjectKey implements the sidebar's "D": armed on a project row,
// confirmed by pressing D again — see the top-level handleKey disarm check,
// which clears the arm on any other key so it can't fire later by accident.
func (m *model) deleteProjectKey() (tea.Model, tea.Cmd) {
	if m.sideCursor >= len(m.side) {
		return m, nil
	}
	it := m.side[m.sideCursor]
	if it.kind != rowProject {
		return m, nil
	}
	if m.confirmDeleteProjectID == it.project.ID {
		pid, name := it.project.ID, it.project.Name
		m.confirmDeleteProjectID = ""
		return m, m.call("project.delete", map[string]any{"project_id": pid}, "deleted "+name)
	}
	m.confirmDeleteProjectID = it.project.ID
	m.status = "press D again to permanently delete " + it.project.Name + " — all its tickets, history, and worktree leases. Any other key cancels."
	return m, nil
}

func statusIcon(s store.TicketStatus) string {
	switch s {
	case store.TicketRunning:
		return "●"
	case store.TicketNeedsInput, store.TicketBlocked:
		return "✋"
	case store.TicketDelivering:
		return "🚀"
	case store.TicketVisiting:
		return "⌨"
	case store.TicketDone:
		return "✓"
	case store.TicketFailed:
		return "✗"
	default:
		return "…"
	}
}

func (m *model) interruptCount() int {
	n := 0
	for _, it := range m.items {
		if it.Tier == store.TierInterrupt {
			n++
		}
	}
	return n
}

func (m *model) sidebarView(height int) string {
	var b strings.Builder
	mode := map[string]string{"heads-down": "🎧", "available": "🟢", "review": "👀"}[m.presence]
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(" ▦ hq "+mode) + "\n")
	for i, it := range m.side {
		var line string
		switch it.kind {
		case rowHeader:
			hdr := styleSideHeader
			if strings.HasPrefix(it.label, "needs you") {
				hdr = styleTierHdr
			}
			b.WriteString("\n" + hdr.Render("— "+it.label+" —") + "\n")
			continue
		case rowItem:
			icon := itemIcon(it.item.Kind)
			line = styleSideTask.Render(fmt.Sprintf("%s %s", icon, truncate(it.item.Title, sidebarWidth-6)))
			if it.item.Tier == store.TierInterrupt {
				line = styleSideChan.Render(fmt.Sprintf("%s %s", icon, truncate(it.item.Title, sidebarWidth-6)))
			}
		case rowAll:
			line = styleSideChan.Render("▤ all projects")
		case rowHome:
			badge := ""
			if it.project.Unread > 0 {
				badge = " " + styleBadgeQuiet.Render(fmt.Sprint(it.project.Unread))
			}
			line = styleSideChan.Render("◆ hq") + badge
		case rowProject:
			// Just the name and, quietly, its unread count. Ticket state
			// lives on the board and in the nested ticket rows — a second
			// number here was noise.
			badge := ""
			pad := 0
			if it.project.Unread > 0 {
				badge = " " + styleBadgeQuiet.Render(fmt.Sprint(it.project.Unread))
				pad = 1 + len(fmt.Sprint(it.project.Unread))
			}
			line = styleSideChan.Render("# "+truncate(it.project.Name, sidebarWidth-4-pad)) + badge
		case rowTicket:
			t := it.ticket
			badge := ""
			pad := 0
			if t.Unread > 0 {
				badge = " " + styleBadge.Render(fmt.Sprint(t.Unread))
				pad = 3 + len(fmt.Sprint(t.Unread))
			}
			line = styleSideTask.Render(fmt.Sprintf("  %s %s", statusIcon(t.Status), truncate(t.Title, sidebarWidth-8-pad))) + badge
		case rowEM:
			line = styleSideTask.Render(" ◉ EM · " + truncate(orDefaultModel(it.project.EMModel), sidebarWidth-9))
		case rowEng:
			line = styleSideTask.Render(fmt.Sprintf(" %s %s", statusIcon(it.ticket.Status), truncate(it.ticket.Title, sidebarWidth-7)))
		}
		switch {
		case i == m.sideCursor && m.focus == focusSidebar:
			// Keyboard focus is here: bright, moveable with j/k.
			line = styleSideSel.Render(stripANSIPad(line, sidebarWidth-2))
		case m.isCurrentRow(it):
			// Not focused here, but this is where you are — still worth
			// marking, or the sidebar looks like it doesn't know where you are.
			line = styleSideActive.Render(stripANSIPad(line, sidebarWidth-2))
		}
		b.WriteString(line + "\n")
	}
	return styleSidebar.Height(height).Render(b.String())
}
