package orch

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gohacki/hq/internal/store"
)

// Session checkout/checkin: hand a live agent session over to the human for
// interactive use, and back. The daemon never owns a terminal itself — it
// just drops the headless process (so exactly one process owns the session)
// and hands the TUI everything it needs to spawn the real interactive CLI
// in its own embedded terminal. Checking back in restores headless
// supervision on the same session id.

// Checkout is what the TUI needs to spawn the real interactive harness CLI
// itself, resumed on the session being handed over.
type Checkout struct {
	Argv []string `json:"argv"`
	Dir  string   `json:"dir"`
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// checkoutEM hands the project's EM (or director) session over for interactive use.
// The headless process (if warm) is dropped first; the next project message
// resumes it headlessly as usual once the human checks back in.
func (o *Orch) checkoutEM(projectID string) (Checkout, error) {
	p, err := o.d.Store.ProjectByID(projectID)
	if err != nil {
		return Checkout{}, err
	}
	if p.EMSessionID == "" {
		return Checkout{}, fmt.Errorf("%s has no manager session yet — message the project first", p.Name)
	}
	o.dropEM(p.ID)
	dir := o.d.Paths.ProjectDir(p.Name)
	// Same MCP tools as the headless EM, so the interactive session can
	// delegate tickets too.
	extra := []string{}
	if mcp := filepath.Join(dir, "mcp.json"); fileExists(mcp) {
		extra = append(extra, "--mcp-config", mcp)
	}
	argv := o.harness.InteractiveCommand(p.EMSessionID, p.EMModel, extra...)
	return Checkout{Argv: argv, Dir: dir}, nil
}

// checkoutEngineer hands a ticket's engineer session over for interactive
// use in its worktree. The headless process is closed first so exactly one
// process owns the session; checkin resumes supervision.
func (o *Orch) checkoutEngineer(ticketID string) (Checkout, error) {
	t, err := o.d.Store.TicketByID(ticketID)
	if err != nil {
		return Checkout{}, err
	}
	if t.SessionID == "" || t.WorktreePath == "" {
		return Checkout{}, fmt.Errorf("ticket has no live session/worktree to visit")
	}

	o.dropEng(t.ID)

	o.mu.Lock()
	o.visitPrev[t.ID] = t.Status
	o.mu.Unlock()

	t.Status = store.TicketVisiting
	if err := o.d.UpdateTicket(t); err != nil {
		o.mu.Lock()
		delete(o.visitPrev, t.ID)
		o.mu.Unlock()
		return Checkout{}, err
	}

	argv := o.harness.InteractiveCommand(t.SessionID, o.engModel(t))
	return Checkout{Argv: argv, Dir: t.WorktreePath}, nil
}

// checkinEM is a no-op: the EM has no visiting status to restore — the next
// project message just resumes the session headlessly as usual.
func (o *Orch) checkinEM(projectID string) error { return nil }

// checkinEngineer restores headless supervision after the human checks back
// in — whether by explicit detach or the interactive process exiting on its
// own. A ticket that was already terminal before checkout returns to that
// status instead of being parked.
func (o *Orch) checkinEngineer(ticketID string) error {
	cur, err := o.d.Store.TicketByID(ticketID)
	if err != nil {
		return err
	}
	if cur.Status != store.TicketVisiting {
		return nil // ticket moved on while checked out
	}

	o.mu.Lock()
	prev, ok := o.visitPrev[ticketID]
	delete(o.visitPrev, ticketID)
	o.mu.Unlock()
	if !ok {
		prev = store.TicketNeedsInput
	}

	if prev.Terminal() {
		cur.Status = prev
		return o.d.UpdateTicket(cur)
	}
	cur.Status = store.TicketNeedsInput
	if err := o.d.UpdateTicket(cur); err != nil {
		return err
	}
	o.systemMessage(cur.ProjectID, cur.ID,
		"desk visit ended — reply in this thread to resume headless supervision")
	return nil
}
