package tui

// vim.go — the vim-native layer: the gg chord, : command-line, / search with
// n/N repeats, and the context-aware jump targets they land on. handleKey
// routes here; nothing in this file is reachable while the composer has
// focus, so typing "g" or ":" in a message is never intercepted.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cmdlineKey captures every keypress while a : command or / search is being
// typed. esc (or backspacing past the start) cancels; enter executes.
func (m *model) cmdlineKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.cmdMode, m.cmdBuf = 0, ""
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		mode, line := m.cmdMode, strings.TrimSpace(m.cmdBuf)
		m.cmdMode, m.cmdBuf = 0, ""
		if line == "" {
			return m, nil
		}
		if mode == ':' {
			return m.execCommand(line)
		}
		m.searchQuery = line
		return m.searchMove(1)
	case "backspace":
		if m.cmdBuf == "" {
			m.cmdMode = 0
			return m, nil
		}
		r := []rune(m.cmdBuf)
		m.cmdBuf = string(r[:len(r)-1])
		return m, nil
	}
	switch k.Type {
	case tea.KeyRunes:
		m.cmdBuf += string(k.Runes)
	case tea.KeySpace:
		m.cmdBuf += " "
	}
	return m, nil
}

// execCommand runs a : command. These mirror the single-key bindings so
// anything reachable by key is also reachable by name.
func (m *model) execCommand(line string) (tea.Model, tea.Cmd) {
	f := strings.Fields(line)
	switch f[0] {
	case "q", "quit":
		return m, tea.Quit
	case "h", "help":
		m.openHelp()
		return m, nil
	case "board", "home":
		m.focus = focusMain
		return m, m.gotoBoard("")
	case "hq", "intake":
		if cr := m.conferenceRoomID(); cr != "" {
			return m, m.openChat(cr, "")
		}
		return m, nil
	case "model":
		return m, m.modelCommand(f[1:])
	case "presence":
		if len(f) > 1 {
			return m, m.call("presence.set", map[string]any{"mode": f[1]}, "presence → "+f[1])
		}
		return m.cyclePresence()
	case "handbook":
		return m.openHandbook()
	}
	m.status = "not a command: :" + f[0] + " (:q :help :board :hq :model :presence :handbook)"
	return m, nil
}

// gotoFirst / gotoLast are gg / G, landing on whatever list the focus is in;
// with no list (a chat timeline), they scroll the viewport instead.
func (m *model) gotoFirst() (tea.Model, tea.Cmd) {
	switch {
	case m.focus == focusSidebar:
		m.sideCursor = 0
		m.skipHeader(1)
		return m, nil
	case m.screen == screenPlan:
		m.planCursor = 0
	case m.screen == screenBoard:
		m.boardSel.row = 0
	default:
		m.vp.GotoTop()
		return m, nil
	}
	m.renderMain()
	return m, nil
}

func (m *model) gotoLast() (tea.Model, tea.Cmd) {
	switch {
	case m.focus == focusSidebar:
		m.sideCursor = len(m.side) - 1
		m.skipHeader(-1)
		return m, nil
	case m.screen == screenPlan:
		m.planCursor = max(0, len(m.planTickets)+len(m.planQs)-1)
	case m.screen == screenBoard:
		cols := m.boardCols()
		if m.boardSel.col >= 0 && m.boardSel.col < len(cols) {
			m.boardSel.row = max(0, len(cols[m.boardSel.col].tickets)-1)
		}
	default:
		m.vp.GotoBottom()
		return m, nil
	}
	m.renderMain()
	return m, nil
}

// searchMove jumps the focused list's cursor to the next (dir > 0) or
// previous match of searchQuery, wrapping vim-style.
func (m *model) searchMove(dir int) (tea.Model, tea.Cmd) {
	q := strings.ToLower(m.searchQuery)
	if q == "" {
		m.status = "no search pattern — / to set one"
		return m, nil
	}
	match := func(ss ...string) bool {
		for _, s := range ss {
			if strings.Contains(strings.ToLower(s), q) {
				return true
			}
		}
		return false
	}
	notFound := func() (tea.Model, tea.Cmd) {
		m.status = "pattern not found: " + m.searchQuery
		return m, nil
	}

	switch {
	case m.focus == focusSidebar:
		var idxs []int
		for i, it := range m.side {
			if it.selectable() && match(sideText(it)) {
				idxs = append(idxs, i)
			}
		}
		if len(idxs) == 0 {
			return notFound()
		}
		m.sideCursor = nextCyclic(idxs, m.sideCursor, dir)
		return m, nil

	case m.screen == screenPlan:
		var idxs []int
		for i, r := range m.planTickets {
			if match(r.t.Title, r.t.Repo) {
				idxs = append(idxs, i)
			}
		}
		for i, qr := range m.planQs {
			if match(qr.q) {
				idxs = append(idxs, len(m.planTickets)+i)
			}
		}
		if len(idxs) == 0 {
			return notFound()
		}
		m.planCursor = nextCyclic(idxs, m.planCursor, dir)

	case m.screen == screenBoard:
		// Flatten matches to scalar positions (column-major) so cyclic
		// next/prev works across columns.
		cols := m.boardCols()
		key := func(s boardSel) int { return s.col*100000 + s.row }
		var sels []boardSel
		var keys []int
		for ci, c := range cols {
			for ri, idx := range c.tickets {
				t := m.all[idx]
				if match(t.Title, t.Project) {
					sels = append(sels, boardSel{ci, ri})
					keys = append(keys, key(boardSel{ci, ri}))
				}
			}
		}
		if len(sels) == 0 {
			return notFound()
		}
		m.boardSel = sels[indexOf(keys, nextCyclic(keys, key(m.boardSel), dir))]

	default:
		m.status = "nothing searchable here — gg/G scroll the timeline"
		return m, nil
	}
	m.renderMain()
	return m, nil
}

// nextCyclic picks the first candidate strictly after cur (dir > 0) or
// strictly before it (dir < 0), wrapping around. candidates is ascending.
func nextCyclic(candidates []int, cur, dir int) int {
	if dir > 0 {
		for _, c := range candidates {
			if c > cur {
				return c
			}
		}
		return candidates[0]
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		if candidates[i] < cur {
			return candidates[i]
		}
	}
	return candidates[len(candidates)-1]
}

func indexOf(xs []int, x int) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return 0
}

// sideText is the searchable text of a sidebar row.
func sideText(it sideItem) string {
	switch it.kind {
	case rowItem:
		if it.item != nil {
			return it.item.Title
		}
	case rowAll:
		return "all projects board"
	case rowHome:
		return "hq intake"
	case rowProject:
		return it.project.Name
	case rowEM:
		return "em manager"
	case rowTicket, rowEng:
		if it.ticket != nil {
			return it.ticket.Title
		}
	}
	return it.label
}
