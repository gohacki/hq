package orch

import (
	"os"
	"os/exec"
	"strings"

	"github.com/gohacki/hq/internal/store"
	"github.com/gohacki/hq/internal/worktree"
)

// teardown removes a finished ticket's whole worktree set — fail-closed:
// any tree with unlanded work is kept (and the boss told why). Branch refs
// live in the user's repo (native worktrees share refs), so removing a
// clean tree never loses committed work.
func (o *Orch) teardown(t store.Ticket) {
	trees, err := o.d.Store.WorktreesForTicket(t.ID)
	if err != nil {
		o.log.Error("worktrees lookup", "ticket", t.ID, "err", err)
		return
	}
	if len(trees) == 0 && t.WorktreePath != "" {
		// Pre-worktree-table ticket (treehouse era): nothing we can remove
		// safely from here; just stop tracking it.
		o.systemMessage(t.ProjectID, t.ID, "legacy worktree left in place: "+t.WorktreePath)
		o.clearPrimaryPath(t)
		return
	}
	if kept := o.removeTicketTrees(t.ID, false); kept == 0 {
		o.clearPrimaryPath(t)
		o.systemMessage(t.ProjectID, t.ID, "worktrees removed — branch kept in the repo(s)")
	}
}

// removeTicketTrees removes every recorded tree for a ticket. Without
// force, trees with unlanded work are kept (row retained so a later sweep
// can retry); returns how many were kept.
func (o *Orch) removeTicketTrees(ticketID string, force bool) (kept int) {
	trees, err := o.d.Store.WorktreesForTicket(ticketID)
	if err != nil {
		o.log.Error("worktrees lookup", "ticket", ticketID, "err", err)
		return 1
	}
	var t store.Ticket
	if cur, err := o.d.Store.TicketByID(ticketID); err == nil {
		t = cur
	}
	for _, w := range trees {
		if !force {
			if reason := unlandedWork(w.Path); reason != "" {
				kept++
				o.systemMessage(t.ProjectID, t.ID, "worktree kept ("+reason+"): "+w.Path)
				continue
			}
		}
		if err := worktree.Remove(w.RepoPath, w.Path); err != nil {
			o.log.Error("worktree remove", "path", w.Path, "err", err)
			o.systemMessage(t.ProjectID, t.ID, "worktree remove failed: "+err.Error())
			kept++
			continue
		}
		if err := o.d.Store.DeleteWorktree(w.ID); err != nil {
			o.log.Error("delete worktree row", "err", err)
		}
	}
	return kept
}

func (o *Orch) clearPrimaryPath(t store.Ticket) {
	cur, err := o.d.Store.TicketByID(t.ID)
	if err != nil {
		o.log.Error("ticket lookup after teardown", "err", err)
		return
	}
	cur.WorktreePath = ""
	if err := o.d.UpdateTicket(cur); err != nil {
		o.log.Error("update ticket after teardown", "err", err)
	}
}

// unlandedWork reports why a worktree is unsafe to remove ("" = safe):
// uncommitted changes, or commits on a detached HEAD that no branch holds.
// A directory that's already gone is trivially safe.
func unlandedWork(wt string) string {
	if _, err := os.Stat(wt); os.IsNotExist(err) {
		return ""
	}
	out, err := exec.Command("git", "-C", wt, "status", "--porcelain").Output()
	if err != nil {
		return "git status failed: " + err.Error()
	}
	if strings.TrimSpace(string(out)) != "" {
		return "uncommitted changes"
	}
	// On a branch → commits are on a ref, safe.
	if br, err := exec.Command("git", "-C", wt, "symbolic-ref", "-q", "--short", "HEAD").Output(); err == nil && strings.TrimSpace(string(br)) != "" {
		return ""
	}
	// Detached HEAD: safe only if some real branch ref contains it.
	out, err = exec.Command("git", "-C", wt, "for-each-ref", "--contains", "HEAD", "refs/heads").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "detached HEAD with commits not on any branch"
	}
	return ""
}

// sweepTeardowns runs at boot: done tickets whose worktrees were never
// removed (e.g. daemon died between classify and teardown).
func (o *Orch) sweepTeardowns() {
	tickets, err := o.d.Store.DoneTicketsWithWorktree()
	if err != nil {
		o.log.Error("teardown sweep query", "err", err)
		return
	}
	for _, t := range tickets {
		o.teardown(t)
	}
}
