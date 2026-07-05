package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gohacki/hq/internal/store"
)

const sidebarWidth = 24

var (
	styleSidebar = lipgloss.NewStyle().Width(sidebarWidth).Padding(0, 1).
			Border(lipgloss.NormalBorder(), false, true, false, false).
			BorderForeground(lipgloss.Color("240"))
	styleFrame      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	styleSideHeader = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Bold(true)
	styleSideSel    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62"))
	styleSideChan   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleSideTask   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleBadge      = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("160")).Padding(0, 1).Bold(true)
	styleBadgeSoft  = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("172")).Padding(0, 1)
	styleHeader     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("236")).Padding(0, 1)
	styleStatus     = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 1)
	styleAuthBoss   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45"))
	styleAuthEM     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	styleAuthEng    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	styleAuthSys    = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Italic(true)
	styleTime       = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleCard       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	styleCardSel    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(0, 1)
	styleReport     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(0, 1)
	styleTierHdr    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
	styleTierHdr2   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("172"))
	styleDim        = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	styleAccepted   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleStruck     = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Strikethrough(true)
	styleCol        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("110"))
)

// layout recomputes pane sizes. The outer frame border eats 2 cols/rows and
// the sidebar's right border 1 col.
func (m *model) layout() {
	innerW, innerH := m.width-2, m.height-2
	mainWidth := innerW - sidebarWidth - 1
	if mainWidth < 20 {
		mainWidth = 20
	}
	composerH := 0
	if m.screen == screenChat || m.planAnswer >= 0 {
		composerH = 5
	}
	vpHeight := innerH - 1 /*header*/ - composerH - 1 /*status*/
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.mainWidth, m.innerH = mainWidth, innerH
	m.vp = viewport.New(mainWidth, vpHeight)
	m.composer.SetWidth(mainWidth - 2)
	m.renderMain()
}

// renderMain fills the viewport for the active screen.
func (m *model) renderMain() {
	if m.vp.Width == 0 || m.showHelp {
		return
	}
	// Composer visibility varies by screen; recompute viewport height.
	composerH := 0
	if m.screen == screenChat || m.planAnswer >= 0 {
		composerH = 5
	}
	h := m.innerH - 1 - composerH - 1
	if h < 3 {
		h = 3
	}
	if m.vp.Height != h {
		m.vp.Height = h
	}
	switch m.screen {
	case screenOffice:
		m.vp.SetContent(m.officeContent())
		m.vp.GotoTop()
	case screenPlan:
		m.vp.SetContent(m.planContent())
		m.vp.GotoTop()
	case screenBoard:
		m.vp.SetContent(m.boardContent())
		m.vp.GotoTop()
	case screenChat:
		m.vp.SetContent(m.chatContent())
		m.vp.GotoBottom()
	}
}

// --- office rendering ---

func itemIcon(k store.ItemKind) string {
	switch k {
	case store.ItemDemo:
		return "🖥"
	case store.ItemPlan:
		return "🗒"
	case store.ItemOptions, store.ItemQuestion:
		return "❓"
	case store.ItemBlocked:
		return "⛔"
	case store.ItemFailed:
		return "✗"
	case store.ItemHandbook:
		return "📘"
	default:
		return "•"
	}
}

func (m *model) officeContent() string {
	if len(m.items) == 0 {
		return styleDim.Render(`
  Nothing needs you.

  The department is either working or idle — check the board (b), open a
  project in the sidebar (tab), or talk to the PM in the conference room.`)
	}
	var b strings.Builder
	tier := store.ItemTier("")
	for i, it := range m.items {
		if it.Tier != tier {
			tier = it.Tier
			hdr := styleTierHdr.Render("▌needs you now")
			if tier == store.TierBreak {
				hdr = styleTierHdr2.Render("▌when you have a minute")
			}
			b.WriteString("\n" + hdr + "\n")
		}
		head := fmt.Sprintf("%s %s · %s · %s", itemIcon(it.Kind), it.Kind, m.projectName(it.ProjectID), age(it.CreatedAt))
		if tt := m.ticketTitle(it.TicketID); tt != "" {
			head += " · " + truncate(tt, 40)
		}
		body := head + "\n" + lipgloss.NewStyle().Bold(true).Render(truncate(it.Title, m.mainWidth-8))
		if i == m.officeCursor {
			detail := strings.TrimSpace(it.Body)
			if detail != "" {
				lines := strings.Split(detail, "\n")
				if len(lines) > 14 {
					lines = append(lines[:14], "… (enter opens the full thread)")
				}
				body += "\n\n" + strings.Join(lines, "\n")
			}
			if it.Kind == store.ItemOptions {
				body += "\n\n" + m.renderOptions(it)
			}
			body += "\n\n" + styleDim.Render(m.itemActionsHint(it))
			b.WriteString(styleCardSel.Width(m.mainWidth-4).Render(body) + "\n")
		} else {
			b.WriteString(styleCard.Width(m.mainWidth-4).Render(body) + "\n")
		}
	}
	return b.String()
}

func (m *model) renderOptions(it store.Item) string {
	var opts []map[string]string
	if json.Unmarshal([]byte(it.Options), &opts) != nil {
		return ""
	}
	var b strings.Builder
	for i, o := range opts {
		line := fmt.Sprintf("  %d. %s", i+1, o["label"])
		if o["detail"] != "" {
			line += styleDim.Render(" — " + o["detail"])
		}
		b.WriteString(line + "\n")
	}
	if m.optionsMode {
		b.WriteString(styleTierHdr.Render("  press 1-" + fmt.Sprint(len(opts)) + " to decide"))
	} else {
		b.WriteString(styleDim.Render("  enter to arm 1-" + fmt.Sprint(len(opts))))
	}
	return b.String()
}

func (m *model) itemActionsHint(it store.Item) string {
	switch it.Kind {
	case store.ItemDemo:
		return "a: approve · enter/o: open thread · v: visit desk · x: dismiss"
	case store.ItemPlan:
		return "enter: review plan · o: open project chat · x: dismiss"
	case store.ItemOptions:
		return "enter: pick 1-9 · o: open thread · x: dismiss"
	case store.ItemHandbook:
		return "a: apply edit · o: open project chat · x: discard"
	case store.ItemFailed, store.ItemBlocked:
		return "r: retry fresh · enter/o: open thread · v: visit desk · x: dismiss"
	default:
		return "enter/o: open thread and reply · v: visit desk · x: dismiss"
	}
}

// --- plan rendering ---

func (m *model) planContent() string {
	var b strings.Builder
	b.WriteString(styleReport.Width(m.mainWidth-4).Render(m.plan.DocMD) + "\n\n")
	b.WriteString(styleTierHdr2.Render("▌proposed tickets") + styleDim.Render("  (space toggles, e in $EDITOR is not supported yet)") + "\n")
	for i, r := range m.planTickets {
		mark, st := "[x]", styleAccepted
		if !r.accepted {
			mark, st = "[ ]", styleStruck
		}
		line := fmt.Sprintf(" %s %s %s — %s", mark, r.t.Kind, r.t.Title, truncate(strings.ReplaceAll(r.t.Brief, "\n", " "), 60))
		line = st.Render(line)
		if i == m.planCursor {
			line = styleSideSel.Render(stripANSIPad(line, m.mainWidth-6))
		}
		b.WriteString(line + "\n")
	}
	if len(m.planQs) > 0 {
		b.WriteString("\n" + styleTierHdr.Render("▌open questions") + styleDim.Render("  (enter to answer)") + "\n")
		for i, q := range m.planQs {
			line := " ? " + q.q
			if q.answer != "" {
				line += styleAccepted.Render("  → " + q.answer)
			}
			if m.planCursor == len(m.planTickets)+i {
				line = styleSideSel.Render(stripANSIPad(line, m.mainWidth-6))
			}
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("\n" + styleDim.Render("A: approve (spawns checked tickets) · R: request changes · esc: back"))
	return b.String()
}

// --- board rendering ---

type boardCol struct {
	name    string
	tickets []int // indices into m.all
}

func (m *model) boardCols() []boardCol {
	cols := []boardCol{{name: "queued"}, {name: "building"}, {name: "delivering"}, {name: "needs you"}, {name: "done"}}
	demoTickets := map[string]bool{}
	for _, it := range m.items {
		if it.Kind == store.ItemDemo {
			demoTickets[it.TicketID] = true
		}
	}
	cutoff := time.Now().Add(-48 * time.Hour).Unix()
	for i, t := range m.all {
		if t.Project == "conference-room" {
			continue
		}
		switch {
		case t.Status == store.TicketQueued:
			cols[0].tickets = append(cols[0].tickets, i)
		case t.Status == store.TicketRunning || t.Status == store.TicketVisiting:
			cols[1].tickets = append(cols[1].tickets, i)
		case t.Status == store.TicketDelivering:
			cols[2].tickets = append(cols[2].tickets, i)
		case t.Status == store.TicketNeedsInput || t.Status == store.TicketBlocked || t.Status == store.TicketFailed:
			cols[3].tickets = append(cols[3].tickets, i)
		case t.Status == store.TicketDone && t.UpdatedAt > cutoff:
			cols[4].tickets = append(cols[4].tickets, i)
		}
	}
	_ = demoTickets
	return cols
}

func (m *model) boardContent() string {
	cols := m.boardCols()
	colW := (m.mainWidth - 2) / len(cols)
	if colW < 14 {
		colW = 14
	}
	var rendered []string
	for ci, c := range cols {
		var b strings.Builder
		b.WriteString(styleCol.Render(fmt.Sprintf("%s (%d)", c.name, len(c.tickets))) + "\n\n")
		for ti, idx := range c.tickets {
			t := m.all[idx]
			line := fmt.Sprintf("%s %s\n%s · %s", statusIcon(t.Status), truncate(t.Title, colW-4), styleDim.Render(t.Project), styleDim.Render(age(t.UpdatedAt)))
			card := styleCard.Width(colW - 2)
			if m.boardSel.col == ci && m.boardSel.row == ti {
				card = styleCardSel.Width(colW - 2)
			}
			b.WriteString(card.Render(line) + "\n")
		}
		rendered = append(rendered, lipgloss.NewStyle().Width(colW).Render(b.String()))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

// --- chat / timeline rendering ---

func authorStyle(author string) (string, lipgloss.Style) {
	switch {
	case author == "boss":
		return "you", styleAuthBoss
	case author == "em":
		return "EM", styleAuthEM
	case author == "pm":
		return "PM", styleAuthEM
	case strings.HasPrefix(author, "eng:"):
		return "engineer", styleAuthEng
	default:
		return "system", styleAuthSys
	}
}

func (m *model) chatContent() string {
	var b strings.Builder
	timeline := m.openTicket != "" && !m.expandChat
	for _, msg := range m.messages {
		name, st := authorStyle(msg.Author)
		ts := time.Unix(msg.CreatedAt, 0).Format("15:04")
		body := msg.Body
		if timeline && strings.HasPrefix(msg.Author, "eng:") && msg.Kind == "text" {
			// Timeline mode: engineer chatter collapses to its first line.
			first := strings.SplitN(strings.TrimSpace(body), "\n", 2)
			body = first[0]
			if len(first) > 1 {
				body += styleDim.Render("  … (x expands)")
			}
			b.WriteString(styleDim.Render("· ") + st.Render(name) + " " + styleTime.Render(ts) + " " +
				lipgloss.NewStyle().Width(m.mainWidth-10).Render(body) + "\n")
			continue
		}
		b.WriteString(st.Render(name) + " " + styleTime.Render(ts) + "\n")
		if msg.Kind == "report" {
			body = styleReport.Width(m.mainWidth - 4).Render(body)
		} else {
			body = lipgloss.NewStyle().Width(m.mainWidth - 2).Render(body)
		}
		b.WriteString(body + "\n\n")
	}
	if len(m.messages) == 0 {
		b.WriteString(styleAuthSys.Render("no messages yet — say something below"))
	}
	return b.String()
}

// --- top-level view ---

func (m *model) headerView() string {
	var name string
	switch m.screen {
	case screenOffice:
		name = fmt.Sprintf("◉ My Office — %d item(s)", len(m.items))
	case screenBoard:
		name = "▤ Department board"
	case screenPlan:
		name = "🗒 Plan review — " + m.projectName(m.plan.ProjectID)
	case screenChat:
		for _, p := range m.projects {
			if p.ID == m.openProject {
				if p.Name == "conference-room" {
					name = "◇ Conference Room — the PM"
				} else {
					name = "# " + p.Name
					repos := make([]string, len(p.Repos))
					for i, r := range p.Repos {
						repos[i] = r.Name
					}
					if len(repos) > 0 {
						name += "  ·  " + strings.Join(repos, ", ") + "  ·  " + p.Delivery + " · verify " + p.Verify
					}
				}
			}
		}
		if m.openTicket != "" {
			for _, t := range m.tickets {
				if t.ID == m.openTicket {
					name += fmt.Sprintf("  ›  %s [%s]", truncate(t.Title, 40), t.Status)
					if t.Model != "" {
						name += " · " + t.Model
					}
				}
			}
		}
	}
	w := m.mainWidth
	if w < 10 {
		w = 10
	}
	return styleHeader.Width(w).Render(name)
}

func (m *model) View() string {
	if !m.ready {
		return "loading…"
	}
	help := map[screen]string{
		screenOffice: "?: help · j/k: cards · enter: open · a: approve · v: visit · r: retry · x: dismiss · b: board · M: presence · tab: sidebar",
		screenChat:   "?: help · enter: compose · x: expand · v: visit · e: handbook · 1-9: promote · esc: back · /model",
		screenPlan:   "space: toggle ticket · enter: answer question · A: approve · R: request changes · esc: back",
		screenBoard:  "h/l j/k: move · enter: open ticket · b/esc: back",
	}[m.screen]
	status := m.status
	if status == "" {
		status = help
	}
	parts := []string{m.headerView(), m.vp.View()}
	if m.screen == screenChat || m.planAnswer >= 0 {
		parts = append(parts, m.composer.View())
	}
	parts = append(parts, styleStatus.Render(truncate(status, m.mainWidth-2)))
	main := lipgloss.JoinVertical(lipgloss.Left, parts...)
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		m.sidebarView(m.innerH),
		lipgloss.NewStyle().Width(m.mainWidth).Height(m.innerH).Render(main),
	)
	return styleFrame.Render(body)
}

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

var _ tea.Model = (*model)(nil)
