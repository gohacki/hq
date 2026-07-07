package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/gohacki/hq/internal/store"
)

const sidebarWidth = 26

// Rosé Pine Moon — matched to the user's terminal theme.
// https://rosepinetheme.com/palette (moon variant)
const (
	rpBase      = lipgloss.Color("#232136")
	rpOverlay   = lipgloss.Color("#393552")
	rpMuted     = lipgloss.Color("#6e6a86")
	rpSubtle    = lipgloss.Color("#908caa")
	rpText      = lipgloss.Color("#e0def4")
	rpLove      = lipgloss.Color("#eb6f92")
	rpGold      = lipgloss.Color("#f6c177")
	rpRose      = lipgloss.Color("#ea9a97")
	rpPine      = lipgloss.Color("#3e8fb0")
	rpFoam      = lipgloss.Color("#9ccfd8")
	rpIris      = lipgloss.Color("#c4a7e7")
	rpHighlight = lipgloss.Color("#44415a") // highlight med — borders, selections
)

var (
	styleSidebar = lipgloss.NewStyle().Width(sidebarWidth).Padding(0, 1).
			Border(lipgloss.NormalBorder(), false, true, false, false).
			BorderForeground(rpHighlight)
	styleFrame      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(rpHighlight)
	styleSideHeader = lipgloss.NewStyle().Foreground(rpMuted).Bold(true)
	styleSideSel    = lipgloss.NewStyle().Bold(true).Foreground(rpText).Background(rpHighlight)
	styleSideActive = lipgloss.NewStyle().Bold(true).Foreground(rpFoam)
	styleSideChan   = lipgloss.NewStyle().Foreground(rpText)
	styleSideTask   = lipgloss.NewStyle().Foreground(rpSubtle)
	styleBadgeSoft  = lipgloss.NewStyle().Foreground(rpBase).Background(rpGold).Padding(0, 1)
	// styleBadgeQuiet marks sidebar unread counts: visible, never shouting.
	styleBadgeQuiet = lipgloss.NewStyle().Foreground(rpGold)
	styleHeader     = lipgloss.NewStyle().Bold(true).Foreground(rpText).Background(rpOverlay).Padding(0, 1)
	styleMeta       = lipgloss.NewStyle().Foreground(rpSubtle).Background(rpOverlay).Padding(0, 1)
	styleStatus     = lipgloss.NewStyle().Foreground(rpSubtle).Padding(0, 1)
	styleAuthBoss   = lipgloss.NewStyle().Bold(true).Foreground(rpFoam)
	styleAuthEM     = lipgloss.NewStyle().Bold(true).Foreground(rpIris)
	styleAuthEng    = lipgloss.NewStyle().Bold(true).Foreground(rpGold)
	styleAuthSys    = lipgloss.NewStyle().Foreground(rpMuted).Italic(true)
	styleTime       = lipgloss.NewStyle().Foreground(rpMuted)
	styleCard       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(rpHighlight).Padding(0, 1)
	styleCardSel    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(rpIris).Padding(0, 1)
	styleReport     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(rpIris).Padding(0, 1)
	styleTierHdr    = lipgloss.NewStyle().Bold(true).Foreground(rpLove)
	styleTierHdr2   = lipgloss.NewStyle().Bold(true).Foreground(rpGold)
	styleDim        = lipgloss.NewStyle().Foreground(rpMuted)
	styleAccepted   = lipgloss.NewStyle().Foreground(rpPine)
	styleStruck     = lipgloss.NewStyle().Foreground(rpMuted).Strikethrough(true)
	styleCol        = lipgloss.NewStyle().Bold(true).Foreground(rpFoam)
	styleColSel     = lipgloss.NewStyle().Bold(true).Foreground(rpText).Underline(true)
	styleURL        = lipgloss.NewStyle().Foreground(rpIris).Underline(true)
	styleBanner     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(rpLove).Padding(0, 1)
)

// headerLines is how many rows the header block takes on the current screen:
// ticket chats get a second metadata line.
func (m *model) headerLines() int {
	if m.screen == screenChat && m.openTicket != "" {
		return 2
	}
	return 1
}

// bannerLines is how many rows the pending-decision banner takes (0 if none).
func (m *model) bannerLines() int {
	if m.screen != screenChat {
		return 0
	}
	if b := m.bannerView(); b != "" {
		return lipgloss.Height(b)
	}
	return 0
}

// layout recomputes pane sizes. The outer frame border eats 2 cols/rows and
// the sidebar's right border 1 col.
func (m *model) layout() {
	innerW, innerH := m.width-2, m.height-2
	mainWidth := innerW - sidebarWidth - 1
	if mainWidth < 20 {
		mainWidth = 20
	}
	m.mainWidth, m.innerH = mainWidth, innerH
	m.vp = viewport.New(mainWidth, m.vpHeight())
	m.composer.SetWidth(mainWidth - 2)
	m.renderMain()
}

func (m *model) vpHeight() int {
	composerH := 0
	if m.screen == screenChat || m.planAnswer >= 0 {
		// Measure, don't guess — a wrong reserve leaves the status line
		// floating above blank padding rows.
		composerH = lipgloss.Height(m.composer.View())
	}
	h := m.innerH - m.headerLines() - m.bannerLines() - composerH - 1 /*status*/
	if h < 3 {
		h = 3
	}
	return h
}

// renderMain fills the viewport for the active screen.
func (m *model) renderMain() {
	if m.vp.Width == 0 || m.showHelp {
		return
	}
	if h := m.vpHeight(); m.vp.Height != h {
		m.vp.Height = h
	}
	switch m.screen {
	case screenDoc:
		m.vp.SetContent(lipgloss.NewStyle().Width(m.mainWidth - 2).Render(m.docBody))
		m.vp.GotoTop()
	case screenPlan:
		m.vp.SetContent(m.planContent())
		m.vp.GotoTop()
	case screenBoard:
		m.vp.SetContent(m.boardContent())
		m.vp.GotoTop()
	case screenChat:
		// Follow the tail only if we were already at it — a refresh must not
		// yank the view back down while the human is reading history.
		atBottom := m.vp.AtBottom()
		m.vp.SetContent(m.chatContent())
		if atBottom {
			m.vp.GotoBottom()
		}
	}
}

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

// --- plan rendering ---

func (m *model) planContent() string {
	var b strings.Builder
	b.WriteString(styleReport.Width(m.mainWidth-4).Render(m.plan.DocMD) + "\n\n")
	b.WriteString(styleTierHdr2.Render("▌proposed tickets") + styleDim.Render("  (space toggles)") + "\n")
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

var reHTTPURL = regexp.MustCompile(`https?://[^\s)>\]"'*]+`)

// Chat messages are markdown, rendered pi-style: a real terminal
// markdown renderer (glamour) with per-message caching so a repaint only
// pays for new content. Falls back to the raw text if rendering fails.
type mdCache struct {
	width    int
	rendered string
}

func (m *model) mdRenderer(width int) *glamour.TermRenderer {
	if m.md != nil && m.mdWidth == width {
		return m.md
	}
	style := styles.DarkStyleConfig
	zero := uint(0)
	style.Document.Margin = &zero
	style.Document.BlockPrefix = ""
	style.Document.BlockSuffix = ""
	// Rosé Pine Moon accents, matching the TUI palette.
	iris, gold, subtle := "#c4a7e7", "#f6c177", "#908caa"
	style.Heading.Color = &iris
	style.H1.Color = &iris
	style.H1.BackgroundColor = nil
	style.Link.Color = &iris
	style.LinkText.Color = &iris
	style.Code.Color = &gold
	style.Code.BackgroundColor = nil
	style.BlockQuote.Color = &subtle
	style.CodeBlock.Theme = "rose-pine-moon"
	r, err := glamour.NewTermRenderer(glamour.WithStyles(style), glamour.WithWordWrap(width), glamour.WithEmoji())
	if err != nil {
		return nil
	}
	m.md, m.mdWidth = r, width
	return r
}

// renderMD renders markdown for the chat at the given width, caching by
// message id (id 0 = uncached, e.g. the live stream bubble).
func (m *model) renderMD(id int64, body string, width int) string {
	if id != 0 {
		if c, ok := m.mdCache[id]; ok && c.width == width {
			return c.rendered
		}
	}
	out := body
	if r := m.mdRenderer(width); r != nil {
		if s, err := r.Render(body); err == nil {
			out = strings.TrimRight(s, "\n")
		}
	}
	if id != 0 {
		if m.mdCache == nil {
			m.mdCache = map[int64]mdCache{}
		}
		m.mdCache[id] = mdCache{width: width, rendered: out}
	}
	return out
}

func (m *model) demoURLs() map[string]string {
	urls := map[string]string{}
	for _, it := range m.items {
		if it.Kind == store.ItemDemo && it.TicketID != "" {
			urls[it.TicketID] = reHTTPURL.FindString(it.Body)
		}
	}
	return urls
}

// boardCols buckets tickets into the kanban's lifecycle columns, scoped to
// m.boardProject when set. Verify = parked on an open demo (your manual
// check); needs-you = questions/blocked/failed.
func (m *model) boardCols() []boardCol {
	cols := []boardCol{{name: "backlog"}, {name: "in progress"}, {name: "verify"}, {name: "needs you"}, {name: "done"}}
	demos := map[string]bool{}
	for _, it := range m.items {
		if it.Kind == store.ItemDemo {
			demos[it.TicketID] = true
		}
	}
	cutoff := time.Now().Add(-48 * time.Hour).Unix()
	for i, t := range m.all {
		if t.Project == store.DirectorRoomName {
			continue
		}
		if m.boardProject != "" && t.ProjectID != m.boardProject {
			continue
		}
		switch {
		case t.Status == store.TicketQueued:
			cols[0].tickets = append(cols[0].tickets, i)
		case t.Status == store.TicketRunning || t.Status == store.TicketVisiting || t.Status == store.TicketDelivering:
			cols[1].tickets = append(cols[1].tickets, i)
		case t.Status == store.TicketNeedsInput && demos[t.ID]:
			cols[2].tickets = append(cols[2].tickets, i)
		case t.Status == store.TicketNeedsInput || t.Status == store.TicketBlocked || t.Status == store.TicketFailed:
			cols[3].tickets = append(cols[3].tickets, i)
		case t.Status == store.TicketDone && t.UpdatedAt > cutoff:
			cols[4].tickets = append(cols[4].tickets, i)
		}
	}
	return cols
}

func (m *model) boardContent() string {
	cols := m.boardCols()
	urls := m.demoURLs()
	colW := (m.mainWidth - 2) / len(cols)
	if colW < 14 {
		colW = 14
	}
	empty := true
	var rendered []string
	for ci, c := range cols {
		var b strings.Builder
		hdr := styleCol.Render(fmt.Sprintf("%s (%d)", c.name, len(c.tickets)))
		if ci == m.boardSel.col && m.focus == focusMain {
			// Mark the column the cursor is in — with card borders alone the
			// position is easy to lose on a sparse board.
			hdr = styleColSel.Render(fmt.Sprintf("▸ %s (%d)", c.name, len(c.tickets)))
		}
		b.WriteString(hdr + "\n\n")
		for ti, idx := range c.tickets {
			empty = false
			t := m.all[idx]
			line := fmt.Sprintf("%s %s", statusIcon(t.Status), truncate(t.Title, colW-4))
			if m.boardProject == "" {
				line += "\n" + styleDim.Render(t.Project)
			}
			line += "\n" + styleDim.Render(age(t.UpdatedAt))
			if u := urls[t.ID]; u != "" {
				line += "\n" + styleURL.Render(truncate(u, colW-4))
			}
			card := styleCard.Width(colW - 2)
			if m.boardSel.col == ci && m.boardSel.row == ti {
				card = styleCardSel.Width(colW - 2)
			}
			b.WriteString(card.Render(line) + "\n")
		}
		rendered = append(rendered, lipgloss.NewStyle().Width(colW).Render(b.String()))
	}
	board := lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
	if empty {
		hint := `

  No tickets here yet.

  Talk to your team: the ◆ hq director in the sidebar creates projects;
  a project's EM plans and opens tickets. The board fills in as they work.`
		board = lipgloss.JoinVertical(lipgloss.Left, board, styleDim.Render(hint))
	}
	return board
}

// --- chat rendering ---

func authorStyle(author string) (string, lipgloss.Style) {
	switch {
	case author == "boss":
		return "you", styleAuthBoss
	case author == "em", author == "pm": // pm: pre-v3 rows
		return "EM", styleAuthEM
	case strings.HasPrefix(author, "eng:"):
		return "engineer", styleAuthEng
	default:
		return "system", styleAuthSys
	}
}

func (m *model) chatContent() string {
	var b strings.Builder
	timeline := m.openTicket != "" && !m.expandChat
	director := m.openProject != "" && m.openProject == m.directorRoomID()
	for _, msg := range m.messages {
		name, st := authorStyle(msg.Author)
		if director && name == "EM" {
			// Same "em" author tag everywhere; the home room's EM is the director.
			name = "director"
		}
		ts := time.Unix(msg.CreatedAt, 0).Format("15:04")
		body := msg.Body

		// System events render as single interleaved event lines — the
		// Slack "· joined" pattern: one scroll tells the ticket's story.
		if msg.Kind == "system" || msg.Author == "system" {
			b.WriteString(styleDim.Render("· ") + styleAuthSys.Render(truncate(strings.ReplaceAll(body, "\n", " "), m.mainWidth-12)) + " " + styleTime.Render(ts) + "\n")
			continue
		}
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
			body = styleReport.Render(m.renderMD(msg.ID, body, m.mainWidth-6))
		} else {
			body = m.renderMD(msg.ID, body, m.mainWidth-2)
		}
		b.WriteString(body + "\n\n")
	}
	// Live activity: the agent's in-progress turn — streaming text renders
	// as a typing bubble, tool calls as a working line. Ephemeral; the real
	// message row replaces it.
	if m.stream.Body != "" {
		name, st := authorStyle(m.stream.Author)
		if director && name == "EM" {
			name = "director"
		}
		if strings.HasPrefix(m.stream.Body, "⚒") {
			b.WriteString(styleDim.Render("· "+name+" ") + styleAuthSys.Render(truncate(strings.ReplaceAll(m.stream.Body, "\n", " "), m.mainWidth-12)) + styleDim.Render(" …") + "\n")
		} else {
			b.WriteString(st.Render(name) + " " + styleDim.Render("typing…") + "\n")
			b.WriteString(m.renderMD(0, m.stream.Body, m.mainWidth-2) + styleDim.Render(" ▌") + "\n\n")
		}
	}
	if len(m.messages) == 0 && m.stream.Body == "" {
		b.WriteString(styleAuthSys.Render("no messages yet — say something below"))
	}
	return b.String()
}

// bannerView renders the pending-decision banner shown above the composer
// when the open chat has an attention item waiting on the boss.
func (m *model) bannerView() string {
	it := m.pendingItem()
	if it == nil {
		return ""
	}
	head := fmt.Sprintf("%s %s · %s", itemIcon(it.Kind), it.Kind, age(it.CreatedAt))
	var hint string
	switch it.Kind {
	case store.ItemDemo:
		hint = "a: approve · d: dismiss"
		if u := reHTTPURL.FindString(it.Body); u != "" {
			head += "  " + styleURL.Render(u)
		}
	case store.ItemHandbook:
		hint = "a: apply edit · d: discard"
	case store.ItemPlan:
		hint = "P: review plan"
	case store.ItemOptions:
		var opts []map[string]string
		json.Unmarshal([]byte(it.Options), &opts)
		var lines []string
		for i, o := range opts {
			l := fmt.Sprintf("%d. %s", i+1, o["label"])
			if o["detail"] != "" {
				l += styleDim.Render(" — " + truncate(o["detail"], m.mainWidth-20))
			}
			lines = append(lines, l)
		}
		return styleBanner.Width(m.mainWidth - 2).Render(
			head + " " + lipgloss.NewStyle().Bold(true).Render(truncate(it.Title, m.mainWidth-24)) + "\n" +
				strings.Join(lines, "\n") + "\n" + styleDim.Render("press 1-"+fmt.Sprint(len(opts))+" to decide"))
	case store.ItemFailed, store.ItemBlocked:
		hint = "r: retry fresh · d: abandon"
	default:
		hint = "reply below to answer"
	}
	return styleBanner.Width(m.mainWidth - 2).Render(
		head + " " + lipgloss.NewStyle().Bold(true).Render(truncate(it.Title, m.mainWidth-30)) + "  " + styleDim.Render(hint))
}

// --- top-level view ---

func (m *model) headerView() string {
	var name string
	switch m.screen {
	case screenBoard:
		if m.boardProject != "" {
			name = "▤ " + m.projectName(m.boardProject)
			for _, p := range m.projects {
				if p.ID == m.boardProject {
					name += "  ·  " + p.Delivery + " · verify " + p.Verify
				}
			}
		} else {
			name = "▤ all projects"
		}
	case screenPlan:
		name = "🗒 Plan review — " + m.projectName(m.plan.ProjectID)
	case screenDoc:
		name = m.docTitle
	case screenChat:
		for _, p := range m.projects {
			if p.ID == m.openProject {
				if p.Name == store.DirectorRoomName {
					name = "◆ hq — director · new projects, cross-project questions"
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
					name += fmt.Sprintf("  ›  %s", truncate(t.Title, 44))
				}
			}
		}
	}
	w := m.mainWidth
	if w < 10 {
		w = 10
	}
	out := styleHeader.Width(w).Render(name)
	if m.screen == screenChat && m.openTicket != "" {
		out += "\n" + styleMeta.Width(w).Render(m.ticketMetaLine())
	}
	return out
}

// ticketMetaLine is the ticket chat's second header row: everything you'd
// want to know at a glance — status, model, branch, worktree, dev URL, age,
// and how to open the engineer's real session.
func (m *model) ticketMetaLine() string {
	for i := range m.tickets {
		t := &m.tickets[i]
		if t.ID != m.openTicket {
			continue
		}
		parts := []string{fmt.Sprintf("%s %s", statusIcon(t.Status), t.Status), t.Kind, orDefaultModel(t.Model)}
		if t.Branch != "" {
			parts = append(parts, "⎇ "+t.Branch)
		}
		if t.WorktreePath != "" {
			parts = append(parts, "⌂ "+shortPath(t.WorktreePath))
		}
		// Dev-server link: the open demo item's URL while one is pending,
		// falling back to the last URL the engineer posted — the link should
		// survive the demo item being resolved while you're still poking.
		if u := m.demoURLs()[t.ID]; u != "" {
			parts = append(parts, styleURL.Render(u))
		} else if u := m.lastEngURL(t.ID); u != "" {
			parts = append(parts, styleURL.Render(u))
		}
		parts = append(parts, age(t.UpdatedAt))
		if t.SessionID != "" {
			parts = append(parts, styleDim.Render("v: open the engineer's session"))
		}
		return strings.Join(parts, "  ·  ")
	}
	return ""
}

// lastEngURL is the most recent URL the ticket's engineer mentioned —
// usually a dev server from a DEMO turn.
func (m *model) lastEngURL(ticketID string) string {
	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := m.messages[i]
		if msg.TicketID != ticketID || !strings.HasPrefix(msg.Author, "eng:") {
			continue
		}
		if u := reHTTPURL.FindString(msg.Body); u != "" {
			return u
		}
	}
	return ""
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func (m *model) View() string {
	if !m.ready {
		return "loading…"
	}
	help := map[screen]string{
		screenBoard: "h/l j/k gg G: move · enter: open ticket · v: live session · /: search · :: cmd · ?: help",
		screenChat:  "i/enter: compose · v: live session · x: expand · e: handbook · esc: back · ?: help",
		screenPlan:  "space: toggle ticket · enter: answer question · A: approve · R: request changes · esc: back",
		screenDoc:   "j/k gg G ctrl+d/u: scroll · esc: back",
	}[m.screen]
	if m.screen == screenChat && m.focus == focusMain {
		help = "── scroll ──  j/k gg G ctrl+d/u: move · i/enter: compose · x: expand · h/esc: sidebar"
	}
	status := m.status
	if m.cmdMode != 0 {
		// A : command or / search being typed lives in the status line,
		// exactly where vim puts it.
		status = string(rune(m.cmdMode)) + m.cmdBuf + "▏"
	} else if status == "" {
		status = help
	}
	main := m.vp.View()
	if m.pickerOpen {
		main = lipgloss.Place(m.mainWidth, m.vp.Height, lipgloss.Center, lipgloss.Center, m.pickerView())
	}
	parts := []string{m.headerView(), main}
	if b := m.bannerView(); b != "" && m.screen == screenChat {
		parts = append(parts, b)
	}
	if m.screen == screenChat || m.planAnswer >= 0 {
		parts = append(parts, m.composer.View())
	}
	parts = append(parts, styleStatus.Render(truncate(status, m.mainWidth-2)))
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		m.sidebarView(m.innerH),
		lipgloss.NewStyle().Width(m.mainWidth).Height(m.innerH).Render(lipgloss.JoinVertical(lipgloss.Left, parts...)),
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
