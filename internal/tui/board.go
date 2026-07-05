package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// The board is a read-mostly department overview; enter zooms into a ticket.

type boardSel struct {
	col, row int
}

func (m *model) boardKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	cols := m.boardCols()
	clamp := func() {
		if m.boardSel.col >= len(cols) {
			m.boardSel.col = len(cols) - 1
		}
		if m.boardSel.col < 0 {
			m.boardSel.col = 0
		}
		n := len(cols[m.boardSel.col].tickets)
		if m.boardSel.row >= n {
			m.boardSel.row = max(0, n-1)
		}
	}
	switch k.String() {
	case "h", "left":
		m.boardSel.col--
		clamp()
		m.renderMain()
		return m, nil
	case "l", "right":
		m.boardSel.col++
		clamp()
		m.renderMain()
		return m, nil
	case "j", "down":
		m.boardSel.row++
		clamp()
		m.renderMain()
		return m, nil
	case "k", "up":
		m.boardSel.row--
		if m.boardSel.row < 0 {
			m.boardSel.row = 0
		}
		m.renderMain()
		return m, nil
	case "enter":
		clamp()
		c := cols[m.boardSel.col]
		if m.boardSel.row < len(c.tickets) {
			t := m.all[c.tickets[m.boardSel.row]]
			return m, m.openChat(t.ProjectID, t.ID)
		}
		return m, nil
	case "esc":
		m.screen = screenOffice
		m.renderMain()
		return m, nil
	}
	return m, nil
}
