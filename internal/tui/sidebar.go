package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gohacki/hq/internal/daemon"
	"github.com/gohacki/hq/internal/store"
)

type rowKind int

const (
	rowOffice rowKind = iota
	rowConference
	rowHeader
	rowProject
	rowTicket // ticket thread nested under the open project
	rowEM     // staff: the open project's manager
	rowEng    // staff: a live engineer (enter = desk visit)
	rowBoard
)

type sideItem struct {
	kind    rowKind
	label   string
	project daemon.ProjectView
	ticket  *daemon.TicketView
}

func (it sideItem) selectable() bool { return it.kind != rowHeader }

func (m *model) rebuildSidebar() {
	m.side = m.side[:0]
	m.side = append(m.side, sideItem{kind: rowOffice}, sideItem{kind: rowBoard})

	var conference, open daemon.ProjectView
	for _, p := range m.projects {
		if p.Name == "conference-room" {
			conference = p
			continue
		}
		if p.ID == m.openProject {
			open = p
		}
	}
	m.side = append(m.side, sideItem{kind: rowConference, project: conference})
	m.side = append(m.side, sideItem{kind: rowHeader, label: "projects"})
	for _, p := range m.projects {
		if p.Name == "conference-room" {
			continue
		}
		m.side = append(m.side, sideItem{kind: rowProject, project: p})
		if p.ID == m.openProject && m.screen == screenChat {
			for i := range m.tickets {
				m.side = append(m.side, sideItem{kind: rowTicket, project: p, ticket: &m.tickets[i]})
			}
		}
	}
	if open.ID != "" && m.screen == screenChat {
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
	case "e":
		return m.openHandbook()
	case "v", "t":
		if m.sideCursor < len(m.side) {
			it := m.side[m.sideCursor]
			switch {
			case it.kind == rowEM:
				return m, m.call("em.visit", map[string]any{"project_id": it.project.ID, "tmux_session": m.tmuxSession}, "")
			case it.ticket != nil:
				return m, m.call("ticket.visit", map[string]any{"ticket_id": it.ticket.ID, "tmux_session": m.tmuxSession}, "")
			}
		}
		return m, nil
	case "esc":
		m.screen = screenOffice
		m.focus = focusMain
		m.renderMain()
		return m, nil
	}
	return m, nil
}

func (m *model) openSideCursor() (tea.Model, tea.Cmd) {
	if m.sideCursor >= len(m.side) {
		return m, nil
	}
	it := m.side[m.sideCursor]
	switch it.kind {
	case rowOffice:
		m.screen = screenOffice
		m.focus = focusMain
		m.renderMain()
		return m, m.refresh()
	case rowBoard:
		m.screen = screenBoard
		m.focus = focusMain
		m.renderMain()
		return m, m.refresh()
	case rowConference, rowProject:
		return m, m.openChat(it.project.ID, "")
	case rowTicket:
		return m, m.openChat(it.project.ID, it.ticket.ID)
	case rowEM:
		return m, m.call("em.visit", map[string]any{"project_id": it.project.ID, "tmux_session": m.tmuxSession}, "")
	case rowEng:
		return m, m.call("ticket.visit", map[string]any{"ticket_id": it.ticket.ID, "tmux_session": m.tmuxSession}, "")
	}
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
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(" ▦ hq "+mode) + "\n\n")
	for i, it := range m.side {
		var line string
		switch it.kind {
		case rowHeader:
			b.WriteString("\n" + styleSideHeader.Render("— "+it.label+" —") + "\n")
			continue
		case rowOffice:
			badge := ""
			if n := m.interruptCount(); n > 0 {
				badge = " " + styleBadge.Render(fmt.Sprint(n))
			} else if len(m.items) > 0 {
				badge = " " + styleBadgeSoft.Render(fmt.Sprint(len(m.items)))
			}
			line = styleSideChan.Render("◉ my office") + badge
		case rowBoard:
			line = styleSideChan.Render("▤ board")
		case rowConference:
			badge := ""
			if it.project.Unread > 0 {
				badge = " " + styleBadge.Render(fmt.Sprint(it.project.Unread))
			}
			line = styleSideChan.Render("◇ conference room") + badge
		case rowProject:
			badge := ""
			pad := 0
			if it.project.Unread > 0 {
				badge = " " + styleBadge.Render(fmt.Sprint(it.project.Unread))
				pad = 3 + len(fmt.Sprint(it.project.Unread))
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
			mdl := it.project.EMModel
			if mdl == "" {
				mdl = "sonnet"
			}
			line = styleSideTask.Render(" ◉ EM · " + truncate(mdl, sidebarWidth-9))
		case rowEng:
			line = styleSideTask.Render(fmt.Sprintf(" %s %s", statusIcon(it.ticket.Status), truncate(it.ticket.Title, sidebarWidth-7)))
		}
		if i == m.sideCursor && m.focus == focusSidebar {
			line = styleSideSel.Render(stripANSIPad(line, sidebarWidth-2))
		}
		b.WriteString(line + "\n")
	}
	return styleSidebar.Height(height).Render(b.String())
}
