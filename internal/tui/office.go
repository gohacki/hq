package tui

// Attention items (the sidebar inbox) and the plan review screen. The old
// full-screen office is gone: items live in the sidebar, and their one-key
// actions work both there and inside the ticket chat they belong to (via
// the pending-decision banner).

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gohacki/hq/internal/store"
)

// openItem is enter on an inbox row: plans open the review screen,
// everything else opens the conversation it came from (options are answered
// there, via the banner's 1-9).
func (m *model) openItem(it *store.Item) (tea.Model, tea.Cmd) {
	if it == nil {
		return m, nil
	}
	switch it.Kind {
	case store.ItemPlan:
		planID := it.RefID
		return m, func() tea.Msg {
			var p store.Plan
			if err := m.cl.Call("plan.get", map[string]any{"id": planID}, &p); err != nil {
				return errMsg{err}
			}
			return planLoaded{p}
		}
	default:
		return m, m.openChat(it.ProjectID, it.TicketID)
	}
}

// approveItem is the one-key positive action: demos get a canned approval
// reply to the engineer; handbook proposals are applied. item.approve does
// the actual work server-side.
func (m *model) approveItem(it *store.Item) (tea.Model, tea.Cmd) {
	if it == nil {
		return m, nil
	}
	if it.Kind != store.ItemDemo && it.Kind != store.ItemHandbook {
		m.status = "a = approve works on demos and handbook edits"
		return m, nil
	}
	return m, m.call("item.approve", map[string]any{"id": it.ID}, "")
}

// retryItem re-opens a failed/blocked ticket fresh and resolves its item.
func (m *model) retryItem(it *store.Item) (tea.Model, tea.Cmd) {
	if it == nil || (it.Kind != store.ItemFailed && it.Kind != store.ItemBlocked) || it.TicketID == "" {
		return m, nil
	}
	ticketID, itemID := it.TicketID, it.ID
	return m, tea.Sequence(
		m.call("ticket.retry", map[string]any{"ticket_id": ticketID}, "retrying"),
		m.call("item.resolve", map[string]any{"id": itemID}, "retried as a fresh ticket"),
	)
}

// dismissItem resolves an item; failed/blocked tickets are abandoned too,
// or they'd sit in the board's needs-you column forever.
func (m *model) dismissItem(it *store.Item) (tea.Model, tea.Cmd) {
	if it == nil {
		return m, nil
	}
	if (it.Kind == store.ItemFailed || it.Kind == store.ItemBlocked) && it.TicketID != "" {
		itemID := it.ID
		return m, tea.Sequence(
			m.call("ticket.abandon", map[string]any{"ticket_id": it.TicketID}, "abandoned"),
			m.call("item.resolve", map[string]any{"id": itemID}, "dismissed"),
		)
	}
	return m, m.call("item.resolve", map[string]any{"id": it.ID}, "dismissed")
}

// pickOption answers an options item with choice n (1-based).
func (m *model) pickOption(it store.Item, n int) (tea.Model, tea.Cmd) {
	var opts []map[string]string
	if err := json.Unmarshal([]byte(it.Options), &opts); err != nil || n < 1 || n > len(opts) {
		m.status = fmt.Sprintf("pick 1-%d", len(opts))
		return m, nil
	}
	choice := opts[n-1]["label"]
	body := fmt.Sprintf("Decision: %s", choice)
	projectID, ticketID, itemID := it.ProjectID, it.TicketID, it.ID
	return m, tea.Sequence(
		func() tea.Msg {
			if err := m.cl.Call("message.send", map[string]any{
				"project_id": projectID, "ticket_id": ticketID, "body": body,
			}, nil); err != nil {
				return errMsg{err}
			}
			return nil
		},
		m.call("item.resolve", map[string]any{"id": itemID}, "answered: "+choice),
	)
}

// pendingItem is the open attention item belonging to the open chat, if any
// — rendered as the banner above the composer.
func (m *model) pendingItem() *store.Item {
	for i := range m.items {
		it := &m.items[i]
		if m.openTicket != "" && it.TicketID == m.openTicket {
			return it
		}
		if m.openTicket == "" && it.ProjectID == m.openProject && it.TicketID == "" {
			return it
		}
	}
	return nil
}

// --- plan review screen ---

func (m *model) openPlanScreen(p store.Plan) tea.Cmd {
	m.plan = p
	m.planTickets = nil
	m.planQs = nil
	var tickets []proposedTicket
	json.Unmarshal([]byte(p.Tickets), &tickets)
	for _, t := range tickets {
		m.planTickets = append(m.planTickets, planTicketRow{t: t, accepted: true})
	}
	var qs []string
	json.Unmarshal([]byte(p.Questions), &qs)
	for _, q := range qs {
		m.planQs = append(m.planQs, planQuestionRow{q: q})
	}
	m.planCursor = 0
	m.planAnswer = -1
	m.screen = screenPlan
	m.focus = focusMain
	m.renderMain()
	return nil
}

func (m *model) planKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := len(m.planTickets) + len(m.planQs)
	switch k.String() {
	case "j", "down":
		if m.planCursor < rows-1 {
			m.planCursor++
			m.renderMain()
		}
		return m, nil
	case "k", "up":
		if m.planCursor > 0 {
			m.planCursor--
			m.renderMain()
		}
		return m, nil
	case "h", "left":
		m.focus = focusSidebar
		return m, nil
	case " ", "enter":
		if m.planCursor < len(m.planTickets) {
			m.planTickets[m.planCursor].accepted = !m.planTickets[m.planCursor].accepted
			m.renderMain()
			return m, nil
		}
		// question row → answer in composer
		m.planAnswer = m.planCursor - len(m.planTickets)
		m.focus = focusComposer
		m.composer.Placeholder = "Answer: " + truncate(m.planQs[m.planAnswer].q, 60)
		return m, m.composer.Focus()
	case "A":
		return m.approvePlan()
	case "R":
		planID := m.plan.ID
		m.focus = focusMain
		cmd := m.gotoBoard(m.plan.ProjectID)
		return m, tea.Batch(cmd, m.call("plan.reject", map[string]any{"id": planID, "note": "please revise — see project chat for details"}, "changes requested"))
	case "esc":
		return m.escBack()
	}
	return m, nil
}

func (m *model) approvePlan() (tea.Model, tea.Cmd) {
	var accepted []int
	for i, r := range m.planTickets {
		if r.accepted {
			accepted = append(accepted, i)
		}
	}
	answers := make([]string, len(m.planQs))
	for i, q := range m.planQs {
		answers[i] = q.answer
	}
	planID := m.plan.ID
	n := len(accepted)
	m.focus = focusMain
	cmd := m.gotoBoard(m.plan.ProjectID)
	return m, tea.Batch(cmd, m.call("plan.approve", map[string]any{
		"id": planID, "accepted": accepted, "answers": answers,
	}, fmt.Sprintf("plan approved — %d ticket(s) opened", n)))
}

// age renders a compact "how long has this been waiting" string.
func age(created int64) string {
	d := time.Since(time.Unix(created, 0))
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func (m *model) projectName(id string) string {
	for _, p := range m.projects {
		if p.ID == id {
			if p.Name == "conference-room" {
				return "hq"
			}
			return p.Name
		}
	}
	return "?"
}

func (m *model) ticketTitle(id string) string {
	for _, t := range m.all {
		if t.ID == id {
			return t.Title
		}
	}
	return ""
}

var _ = strings.TrimSpace
