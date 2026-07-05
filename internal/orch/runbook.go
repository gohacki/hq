package orch

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gohacki/shipyard/internal/store"
)

// Runbooks (the "## Local development — <repo>" sections) are cached per
// repo so a repo scouted by one channel is never re-scouted by another.
// They stay alive three ways: the cache copy on channel creation, the lead's
// refresh_runbook tool, and every ship brief's self-heal rule.

func (o *Orch) runbookCachePath(repoPath string) string {
	sum := sha256.Sum256([]byte(repoPath))
	return filepath.Join(o.d.Paths.RunbooksDir(), fmt.Sprintf("%x-%s.md", sum[:4], filepath.Base(repoPath)))
}

// seedRunbook gives a channel its repo runbook: copied from the per-repo
// cache when available, otherwise scouted (which also fills the cache).
func (o *Orch) seedRunbook(ctx context.Context, ch store.Channel, repo store.Repo) {
	cache := o.runbookCachePath(repo.Path)
	if b, err := os.ReadFile(cache); err == nil && strings.TrimSpace(string(b)) != "" {
		f, err := os.OpenFile(ch.InstructionsPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "\n%s\n", strings.TrimSpace(string(b)))
			f.Close()
			o.systemMessage(ch.ID, "", "dev runbook for "+repo.Name+" copied from cache (another channel scouted it) — ask me to refresh_runbook if it's stale")
			return
		}
		o.log.Error("append cached runbook", "err", err)
	}
	if err := os.MkdirAll(o.d.Paths.RunbooksDir(), 0o755); err != nil {
		o.log.Error("runbooks dir", "err", err)
		return
	}
	if _, err := o.createTask(ctx, ch.ID, repo.Name, "scout",
		"Map local development workflow ("+repo.Name+")", runbookBrief(ch, repo, cache, false), ""); err != nil {
		o.log.Error("runbook scout spawn failed", "repo", repo.Name, "err", err)
	}
}

// refreshRunbook re-scouts a repo's dev workflow, replacing both the cache
// and this channel's instructions section.
func (o *Orch) refreshRunbook(ctx context.Context, channelID, repoName string) (store.Task, error) {
	ch, err := o.d.Store.ChannelByID(channelID)
	if err != nil {
		return store.Task{}, err
	}
	repos, err := o.d.Store.ReposForChannel(channelID)
	if err != nil {
		return store.Task{}, err
	}
	var repo store.Repo
	if repoName == "" && len(repos) == 1 {
		repo = repos[0]
	} else {
		for _, r := range repos {
			if r.Name == repoName {
				repo = r
			}
		}
	}
	if repo.ID == "" {
		return store.Task{}, fmt.Errorf("unknown repo %q in #%s", repoName, ch.Name)
	}
	if err := os.MkdirAll(o.d.Paths.RunbooksDir(), 0o755); err != nil {
		return store.Task{}, err
	}
	return o.createTask(ctx, ch.ID, repo.Name, "scout",
		"Refresh local development runbook ("+repo.Name+")",
		runbookBrief(ch, repo, o.runbookCachePath(repo.Path), true), "")
}
