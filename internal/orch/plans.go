package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gohacki/hq/internal/store"
)

// The planning flow: an EM proposes a plan (doc + proposed tickets + open
// questions); it lands in My Office as an interrupt; the boss reviews it in
// the plan screen — toggling tickets, answering questions — and approves or
// requests changes. Approval spawns exactly the accepted tickets and reports
// the boss's edits back to the EM's session.

// ProposedTicket is one row of a plan's ticket list.
type ProposedTicket struct {
	Repo  string `json:"repo"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Brief string `json:"brief"`
}

func (o *Orch) proposePlan(projectID, docMD string, tickets []ProposedTicket, questions []string) (store.Plan, error) {
	p, err := o.d.Store.ProjectByID(projectID)
	if err != nil {
		return store.Plan{}, err
	}
	if strings.TrimSpace(docMD) == "" || len(tickets) == 0 {
		return store.Plan{}, fmt.Errorf("a plan needs a doc and at least one proposed ticket")
	}
	for i, t := range tickets {
		if t.Kind != "build" && t.Kind != "spike" {
			return store.Plan{}, fmt.Errorf("ticket %d: kind must be build or spike", i+1)
		}
		if strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Brief) == "" {
			return store.Plan{}, fmt.Errorf("ticket %d: title and brief are required", i+1)
		}
	}
	tb, err := json.Marshal(tickets)
	if err != nil {
		return store.Plan{}, err
	}
	qb, err := json.Marshal(questions)
	if err != nil {
		return store.Plan{}, err
	}
	plan := store.Plan{
		ProjectID: projectID,
		DocMD:     docMD,
		Tickets:   string(tb),
		Questions: string(qb),
	}
	plan, err = o.d.Store.CreatePlan(plan)
	if err != nil {
		return store.Plan{}, err
	}
	docPath := filepath.Join(o.d.Paths.ProjectDir(p.Name), plan.ID+".md")
	if err := os.WriteFile(docPath, []byte(docMD), 0o644); err != nil {
		o.log.Error("write plan doc", "err", err)
	}
	o.systemMessage(p.ID, "", fmt.Sprintf("plan proposed (%d tickets) — review it in your inbox", len(tickets)))
	if _, err := o.d.FileItem(store.Item{
		Kind: store.ItemPlan, Tier: store.TierInterrupt, ProjectID: p.ID, RefID: plan.ID,
		Title: fmt.Sprintf("plan review — %s (%d tickets)", p.Name, len(tickets)),
		Body:  docMD,
	}); err != nil {
		o.log.Error("file plan item", "err", err)
	}
	return plan, nil
}

// approvePlan spawns the accepted tickets and reports the boss's decisions
// (accepted/struck tickets, answers, note) back to the EM.
func (o *Orch) approvePlan(ctx context.Context, planID string, accepted []int, answers []string, note string) (any, error) {
	plan, err := o.d.Store.PlanByID(planID)
	if err != nil {
		return nil, err
	}
	if plan.Status != store.PlanPending {
		return nil, fmt.Errorf("plan is already %s", plan.Status)
	}
	var tickets []ProposedTicket
	if err := json.Unmarshal([]byte(plan.Tickets), &tickets); err != nil {
		return nil, err
	}
	prj, err := o.d.Store.ProjectByID(plan.ProjectID)
	if err != nil {
		return nil, err
	}
	acceptedSet := map[int]bool{}
	for _, i := range accepted {
		if i < 0 || i >= len(tickets) {
			return nil, fmt.Errorf("accepted index %d out of range", i)
		}
		acceptedSet[i] = true
	}

	type result struct {
		Title string `json:"title"`
		ID    string `json:"id,omitempty"`
		Error string `json:"error,omitempty"`
	}
	var spawned []result
	var struck []string
	for i, pt := range tickets {
		if !acceptedSet[i] {
			struck = append(struck, pt.Title)
			continue
		}
		t, err := o.createTicket(ctx, plan.ProjectID, pt.Repo, pt.Kind, pt.Title, pt.Brief, "")
		r := result{Title: pt.Title}
		if err != nil {
			r.Error = err.Error()
		} else {
			r.ID = t.ID
		}
		spawned = append(spawned, r)
	}
	if err := o.d.Store.SetPlanStatus(plan.ID, store.PlanApproved); err != nil {
		return nil, err
	}
	o.resolvePlanItems(plan)

	// Report the outcome to the EM so its session knows what the boss chose.
	var qs []string
	json.Unmarshal([]byte(plan.Questions), &qs)
	var fb strings.Builder
	fb.WriteString("(plan " + plan.ID + " approved by the boss)\n")
	for _, r := range spawned {
		if r.Error != "" {
			fb.WriteString("- FAILED to open: " + r.Title + " — " + r.Error + "\n")
		} else {
			fb.WriteString("- opened: " + r.Title + " (" + r.ID + ")\n")
		}
	}
	for _, s := range struck {
		fb.WriteString("- struck: " + s + "\n")
	}
	for i, a := range answers {
		if a != "" && i < len(qs) {
			fb.WriteString("- answer to \"" + qs[i] + "\": " + a + "\n")
		}
	}
	if note != "" {
		fb.WriteString("- note: " + note + "\n")
	}
	o.systemMessage(prj.ID, "", "plan approved — "+fmt.Sprintf("%d opened, %d struck", len(spawned), len(struck)))
	if err := o.sendToEM(ctx, prj, fb.String()); err != nil {
		o.log.Error("plan feedback to em", "err", err)
	}
	return spawned, nil
}

func (o *Orch) rejectPlan(ctx context.Context, planID, note string) error {
	plan, err := o.d.Store.PlanByID(planID)
	if err != nil {
		return err
	}
	if plan.Status != store.PlanPending {
		return fmt.Errorf("plan is already %s", plan.Status)
	}
	if err := o.d.Store.SetPlanStatus(plan.ID, store.PlanRejected); err != nil {
		return err
	}
	o.resolvePlanItems(plan)
	prj, err := o.d.Store.ProjectByID(plan.ProjectID)
	if err != nil {
		return err
	}
	o.systemMessage(prj.ID, "", "plan changes requested")
	msg := "(the boss requested changes to plan " + plan.ID + ")"
	if note != "" {
		msg += "\n" + note
	}
	return o.sendToEM(ctx, prj, msg)
}

func (o *Orch) resolvePlanItems(plan store.Plan) {
	items, err := o.d.Store.OpenItems()
	if err != nil {
		return
	}
	for _, it := range items {
		if it.Kind == store.ItemPlan && it.RefID == plan.ID {
			if err := o.d.ResolveItem(it.ID); err != nil {
				o.log.Error("resolve plan item", "err", err)
			}
		}
	}
}

// askBoss files a question (optionally with options) into My Office. The
// answer arrives as a normal message in the project scroll / ticket thread.
func (o *Orch) askBoss(projectID, ticketID, question string, options []map[string]string) (store.Item, error) {
	kind := store.ItemQuestion
	optJSON := ""
	if len(options) > 0 {
		kind = store.ItemOptions
		b, err := json.Marshal(options)
		if err != nil {
			return store.Item{}, err
		}
		optJSON = string(b)
	}
	return o.d.FileItem(store.Item{
		Kind: kind, Tier: store.TierInterrupt, ProjectID: projectID, TicketID: ticketID,
		Title: question, Options: optJSON,
	})
}

// proposeHandbookEdit files a break-tier office item carrying a proposed new
// handbook body; approval applies it atomically.
func (o *Orch) proposeHandbookEdit(projectID, summary, newBody string) (store.Item, error) {
	p, err := o.d.Store.ProjectByID(projectID)
	if err != nil {
		return store.Item{}, err
	}
	if strings.TrimSpace(newBody) == "" {
		return store.Item{}, fmt.Errorf("new handbook body is empty")
	}
	pending := filepath.Join(o.d.Paths.ProjectDir(p.Name), "handbook.pending.md")
	if err := os.WriteFile(pending, []byte(newBody), 0o644); err != nil {
		return store.Item{}, err
	}
	return o.d.FileItem(store.Item{
		Kind: store.ItemHandbook, Tier: store.TierBreak, ProjectID: p.ID, RefID: pending,
		Title: "handbook edit — " + summary, Body: newBody,
	})
}

// approveItem is the single, correct "approve" action for any office item:
// for a demo, tells the engineer to finish (and resolves the card); for a
// handbook proposal, applies the edit. It exists so every caller — the
// TUI's 'a' key, `hq call`, anything else — gets the right sequence by
// construction, instead of composing "send this exact message, then
// resolve the item" by hand and risking getting it wrong or forgetting a
// step (which is exactly how the gap this closes was found).
func (o *Orch) approveItem(ctx context.Context, itemID string) (string, error) {
	it, err := o.d.Store.ItemByID(itemID)
	if err != nil {
		return "", err
	}
	switch it.Kind {
	case store.ItemDemo:
		body := "Verified — looks good. Proceed (finish delivery if pending, then STATUS: done)."
		if _, err := o.d.PostMessage(store.Message{
			ProjectID: it.ProjectID, TicketID: it.TicketID, Author: "boss", Body: body,
		}); err != nil {
			return "", err
		}
		if it.TicketID == "" {
			p, err := o.d.Store.ProjectByID(it.ProjectID)
			if err != nil {
				return "", err
			}
			go o.BossProjectMessage(context.WithoutCancel(ctx), p, body)
		} else {
			t, err := o.d.Store.TicketByID(it.TicketID)
			if err != nil {
				return "", err
			}
			go o.BossTicketMessage(context.WithoutCancel(ctx), t, body)
		}
		if err := o.d.ResolveItem(it.ID); err != nil {
			return "", err
		}
		return "demo approved", nil
	case store.ItemHandbook:
		if err := o.applyHandbookEdit(it.ID); err != nil {
			return "", err
		}
		return "handbook updated", nil
	default:
		return "", fmt.Errorf("item %s (%s) isn't something 'approve' applies to — demos and handbook edits only", itemID, it.Kind)
	}
}

// applyHandbookEdit is the office 'approve' action for a handbook item.
func (o *Orch) applyHandbookEdit(itemID string) error {
	it, err := o.d.Store.ItemByID(itemID)
	if err != nil {
		return err
	}
	if it.Kind != store.ItemHandbook {
		return fmt.Errorf("item %s is not a handbook proposal", itemID)
	}
	p, err := o.d.Store.ProjectByID(it.ProjectID)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(it.RefID)
	if err != nil {
		return fmt.Errorf("pending handbook unreadable: %w", err)
	}
	if err := os.WriteFile(p.HandbookPath, body, 0o644); err != nil {
		return err
	}
	os.Remove(it.RefID)
	o.systemMessage(p.ID, "", "handbook updated: "+strings.TrimPrefix(it.Title, "handbook edit — "))
	return o.d.ResolveItem(it.ID)
}

func (o *Orch) registerPlanHandlers() {
	srv := o.d.Server

	srv.Handle("plan.propose", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string           `json:"project_id"`
			DocMD     string           `json:"doc_md"`
			Tickets   []ProposedTicket `json:"tickets"`
			Questions []string         `json:"questions"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.proposePlan(p.ProjectID, p.DocMD, p.Tickets, p.Questions)
	})

	srv.Handle("plan.get", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.d.Store.PlanByID(p.ID)
	})

	srv.Handle("plan.approve", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ID       string   `json:"id"`
			Accepted []int    `json:"accepted"` // indices into the plan's tickets
			Answers  []string `json:"answers"`  // aligned with the plan's questions
			Note     string   `json:"note"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.approvePlan(context.WithoutCancel(ctx), p.ID, p.Accepted, p.Answers, p.Note)
	})

	srv.Handle("plan.reject", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ID   string `json:"id"`
			Note string `json:"note"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return "changes requested", o.rejectPlan(context.WithoutCancel(ctx), p.ID, p.Note)
	})

	srv.Handle("ask.boss", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string              `json:"project_id"`
			TicketID  string              `json:"ticket_id"`
			Question  string              `json:"question"`
			Options   []map[string]string `json:"options"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.askBoss(p.ProjectID, p.TicketID, p.Question, p.Options)
	})

	srv.Handle("handbook.propose", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
			Summary   string `json:"summary"`
			NewBody   string `json:"new_body"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.proposeHandbookEdit(p.ProjectID, p.Summary, p.NewBody)
	})

	srv.Handle("handbook.apply", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ItemID string `json:"item_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return "applied", o.applyHandbookEdit(p.ItemID)
	})

	srv.Handle("item.approve", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.approveItem(context.WithoutCancel(ctx), p.ID)
	})

	srv.Handle("ticket.handoff", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			TicketID string `json:"ticket_id"`
			Proposal string `json:"proposal"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.handoff(context.WithoutCancel(ctx), p.TicketID, p.Proposal)
	})
}
