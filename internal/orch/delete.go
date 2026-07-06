package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gohacki/hq/internal/daemon"
)

func (o *Orch) registerDeleteHandler() {
	o.d.Server.Handle("project.delete", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
			Force     bool   `json:"force"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.deleteProject(p.ProjectID, p.Force)
	})
}

// deleteProject removes a project entirely: kills its EM and any live
// engineers, removes their worktree sets (fail-closed on unlanded work,
// same as normal ticket teardown, unless force), then deletes the DB rows
// and the on-disk project data dir. It never touches the actual repos on
// disk — only hq's own records and worktrees; ticket branches stay in the
// repos.
func (o *Orch) deleteProject(projectID string, force bool) (string, error) {
	p, err := o.d.Store.ProjectByID(projectID)
	if err != nil {
		return "", err
	}
	if p.Name == daemon.ConferenceRoomName {
		return "", fmt.Errorf("the hq home chat can't be deleted")
	}

	tickets, err := o.d.Store.TicketsForProject(projectID)
	if err != nil {
		return "", err
	}

	o.dropEM(projectID)
	for _, t := range tickets {
		o.dropEng(t.ID)
	}

	var blocked []string
	for _, t := range tickets {
		trees, err := o.d.Store.WorktreesForTicket(t.ID)
		if err != nil {
			return "", err
		}
		if !force {
			for _, w := range trees {
				if reason := unlandedWork(w.Path); reason != "" {
					blocked = append(blocked, fmt.Sprintf("%s (%s): %s", t.Title, w.Path, reason))
				}
			}
		}
	}
	if len(blocked) > 0 {
		return "", fmt.Errorf("worktree(s) have unlanded work — pass force to delete anyway and discard it:\n- %s", strings.Join(blocked, "\n- "))
	}
	for _, t := range tickets {
		o.removeTicketTrees(t.ID, force)
	}

	if err := o.d.Store.DeleteProject(projectID); err != nil {
		return "", err
	}
	if dir := o.d.Paths.ProjectDir(p.Name); dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			o.log.Error("remove project dir", "project", p.Name, "err", err)
		}
	}

	return fmt.Sprintf("deleted %s (%d ticket(s))", p.Name, len(tickets)), nil
}
