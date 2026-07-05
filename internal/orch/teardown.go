package orch

import (
	"os/exec"
	"strings"

	"github.com/gohacki/shipyard/internal/store"
	"github.com/gohacki/shipyard/internal/worktree"
)

// teardown returns a finished task's worktree to the treehouse pool —
// fail-closed: the lease is kept (and the captain told why) unless the work
// is provably landed. Branch refs live in the shared repo, so returning a
// clean worktree never loses committed work on a branch.
func (o *Orch) teardown(t store.Task) {
	if t.WorktreePath == "" {
		return
	}
	if reason := unlandedWork(t.WorktreePath); reason != "" {
		o.systemMessage(t.ChannelID, t.ID, "worktree kept ("+reason+"): "+t.WorktreePath)
		return
	}
	if err := worktree.Return(t.WorktreePath); err != nil {
		o.log.Error("worktree return failed", "task", t.ID, "err", err)
		o.systemMessage(t.ChannelID, t.ID, "worktree return failed: "+err.Error())
		return
	}
	cur, err := o.d.Store.TaskByID(t.ID)
	if err != nil {
		o.log.Error("task lookup after teardown", "err", err)
		return
	}
	cur.WorktreePath = ""
	if err := o.d.UpdateTask(cur); err != nil {
		o.log.Error("update task after teardown", "err", err)
	}
	o.systemMessage(t.ChannelID, t.ID, "worktree returned to pool")
}

// unlandedWork reports why a worktree is unsafe to return ("" = safe):
// uncommitted changes, or commits on a detached HEAD that no branch holds.
func unlandedWork(wt string) string {
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

// sweepTeardowns runs at boot: done tasks whose worktrees were never returned
// (e.g. daemon died between classify and teardown).
func (o *Orch) sweepTeardowns() {
	tasks, err := o.d.Store.DoneTasksWithWorktree()
	if err != nil {
		o.log.Error("teardown sweep query", "err", err)
		return
	}
	for _, t := range tasks {
		o.teardown(t)
	}
}
