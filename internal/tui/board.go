package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// The kanban board is hq's home screen: columns are the ticket lifecycle,
// scoped to one project (sidebar enter) or the whole department. Verify
// holds demos parked for the boss's manual check — those cards carry the
// engineer's dev-server URL. enter zooms into the ticket's chat.

type boardSel struct {
	col, row int
}

// nextNonEmpty walks from col+dir in steps of dir and returns the first
// column holding tickets, or -1. Empty columns are never navigation stops.
func nextNonEmpty(cols []boardCol, col, dir int) int {
	for c := col + dir; c >= 0 && c < len(cols); c += dir {
		if len(cols[c].tickets) > 0 {
			return c
		}
	}
	return -1
}

// normalizeBoardSel snaps the cursor off an empty column (tickets moved, or
// the scope changed) onto the first non-empty one, so it's always visible.
func (m *model) normalizeBoardSel() {
	cols := m.boardCols()
	if m.boardSel.col < 0 || m.boardSel.col >= len(cols) {
		m.boardSel = boardSel{}
	}
	if len(cols[m.boardSel.col].tickets) == 0 {
		if c := nextNonEmpty(cols, -1, 1); c >= 0 {
			m.boardSel = boardSel{col: c}
		}
	}
	if n := len(cols[m.boardSel.col].tickets); m.boardSel.row >= n {
		m.boardSel.row = max(0, n-1)
	}
}

func (m *model) boardKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.normalizeBoardSel()
	cols := m.boardCols()
	clampRow := func() {
		n := len(cols[m.boardSel.col].tickets)
		if m.boardSel.row >= n {
			m.boardSel.row = max(0, n-1)
		}
	}
	switch k.String() {
	case "h", "left":
		// Skip empty columns; past the last non-empty one lies the sidebar.
		if c := nextNonEmpty(cols, m.boardSel.col, -1); c >= 0 {
			m.boardSel.col = c
			clampRow()
			m.renderMain()
			return m, nil
		}
		m.focus = focusSidebar
		m.renderMain() // drop the column marker
		return m, nil
	case "l", "right":
		if c := nextNonEmpty(cols, m.boardSel.col, 1); c >= 0 {
			m.boardSel.col = c
			clampRow()
			m.renderMain()
		}
		return m, nil
	case "0", "^":
		if c := nextNonEmpty(cols, -1, 1); c >= 0 {
			m.boardSel.col = c
			clampRow()
			m.renderMain()
		}
		return m, nil
	case "$":
		if c := nextNonEmpty(cols, len(cols), -1); c >= 0 {
			m.boardSel.col = c
			clampRow()
			m.renderMain()
		}
		return m, nil
	case "j", "down":
		m.boardSel.row++
		clampRow()
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
		clampRow()
		c := cols[m.boardSel.col]
		if m.boardSel.row < len(c.tickets) {
			t := m.all[c.tickets[m.boardSel.row]]
			return m, m.openChat(t.ProjectID, t.ID)
		}
		return m, nil
	case "esc":
		// First esc from the cards goes to the sidebar (visible cursor);
		// esc there unscopes the board.
		m.focus = focusSidebar
		m.renderMain()
		return m, nil
	}
	return m, nil
}

// selectedBoardTicket returns the ticket under the board cursor, if any.
func (m *model) selectedBoardTicket() (projectID, ticketID string) {
	cols := m.boardCols()
	if m.boardSel.col < 0 || m.boardSel.col >= len(cols) {
		return "", ""
	}
	c := cols[m.boardSel.col]
	if m.boardSel.row < 0 || m.boardSel.row >= len(c.tickets) {
		return "", ""
	}
	t := m.all[c.tickets[m.boardSel.row]]
	return t.ProjectID, t.ID
}
