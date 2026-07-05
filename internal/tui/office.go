package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gohacki/hq/internal/store"
)

// My Office: the decision queue. Cards render newest-context-first per tier
// (interrupts, then breaks); every action is one key.

func (m *model) selectedItem() *store.Item {
	if m.officeCursor < 0 || m.officeCursor >= len(m.items) {
		return nil
	}
	return &m.items[m.officeCursor]
}

func (m *model) officeKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	it := m.selectedItem()

	if m.optionsMode && it != nil && it.Kind == store.ItemOptions {
		switch s := k.String(); s {
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			return m.pickOption(*it, int(s[0]-'0'))
		case "esc":
			m.optionsMode = false
			m.renderMain()
			return m, nil
		}
		return m, nil
	}

	switch k.String() {
	case "j", "down":
		if m.officeCursor < len(m.items)-1 {
			m.officeCursor++
			m.renderMain()
		}
		return m, nil
	case "k", "up":
		if m.officeCursor > 0 {
			m.officeCursor--
			m.renderMain()
		}
		return m, nil
	case "enter":
		return m.openItem()
	case "a":
		return m.approveItem()
	case "o":
		if it != nil {
			return m, m.openChat(it.ProjectID, it.TicketID)
		}
		return m, nil
	case "v":
		if it != nil && it.TicketID != "" {
			return m, m.call("ticket.visit", map[string]any{"ticket_id": it.TicketID, "tmux_session": m.tmuxSession}, "")
		}
		return m, nil
	case "r":
		if it != nil && (it.Kind == store.ItemFailed || it.Kind == store.ItemBlocked) && it.TicketID != "" {
			ticketID := it.TicketID
			itemID := it.ID
			return m, tea.Sequence(
				m.call("ticket.retry", map[string]any{"ticket_id": ticketID}, "retrying"),
				m.call("item.resolve", map[string]any{"id": itemID}, "retried as a fresh ticket"),
			)
		}
		return m, nil
	case "x":
		if it != nil {
			return m, m.call("item.resolve", map[string]any{"id": it.ID}, "dismissed")
		}
		return m, nil
	}
	return m, nil
}

// openItem is enter on a card: plans open the review screen, options arm the
// 1-9 picker, everything else opens the conversation it came from.
func (m *model) openItem() (tea.Model, tea.Cmd) {
	it := m.selectedItem()
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
	case store.ItemOptions:
		m.optionsMode = true
		m.renderMain()
		return m, nil
	default:
		return m, m.openChat(it.ProjectID, it.TicketID)
	}
}

// approveItem is the one-key positive action: demos get a canned approval
// reply to the engineer; handbook proposals are applied.
func (m *model) approveItem() (tea.Model, tea.Cmd) {
	it := m.selectedItem()
	if it == nil {
		return m, nil
	}
	switch it.Kind {
	case store.ItemDemo:
		projectID, ticketID, itemID := it.ProjectID, it.TicketID, it.ID
		return m, tea.Sequence(
			func() tea.Msg {
				if err := m.cl.Call("message.send", map[string]any{
					"project_id": projectID, "ticket_id": ticketID,
					"body": "Verified — looks good. Proceed (finish delivery if pending, then STATUS: done).",
				}, nil); err != nil {
					return errMsg{err}
				}
				return nil
			},
			m.call("item.resolve", map[string]any{"id": itemID}, "demo approved"),
		)
	case store.ItemHandbook:
		return m, m.call("handbook.apply", map[string]any{"item_id": it.ID}, "handbook updated")
	default:
		m.status = "a = approve works on demos and handbook edits; enter opens this one"
		return m, nil
	}
}

// pickOption answers an options item with choice n (1-based).
func (m *model) pickOption(it store.Item, n int) (tea.Model, tea.Cmd) {
	var opts []map[string]string
	if err := json.Unmarshal([]byte(it.Options), &opts); err != nil || n < 1 || n > len(opts) {
		m.status = fmt.Sprintf("pick 1-%d", len(opts))
		return m, nil
	}
	m.optionsMode = false
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

// --- plan review screen ---

func (m *model) openPlanScreen(p store.Plan) {
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
		m.screen = screenOffice
		m.renderMain()
		return m, m.call("plan.reject", map[string]any{"id": planID, "note": "please revise — see project chat for details"}, "changes requested")
	case "esc":
		m.screen = screenOffice
		m.renderMain()
		return m, nil
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
	m.screen = screenOffice
	m.renderMain()
	return m, m.call("plan.approve", map[string]any{
		"id": planID, "accepted": accepted, "answers": answers,
	}, fmt.Sprintf("plan approved — %d ticket(s) opened", n))
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
