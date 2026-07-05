package orch

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gohacki/hq/internal/store"
)

// Onboarding docs (the "## Local development — <repo>" handbook sections)
// are cached per repo so a repo scouted by one project is never re-scouted
// by another. They stay alive three ways: the cache copy on project
// creation, refresh_onboarding, and every build brief's self-heal rule.

func (o *Orch) onboardingCachePath(repoPath string) string {
	sum := sha256.Sum256([]byte(repoPath))
	return filepath.Join(o.d.Paths.OnboardingDir(), fmt.Sprintf("%x-%s.md", sum[:4], filepath.Base(repoPath)))
}

// seedOnboarding gives a project its repo onboarding doc: copied from the
// per-repo cache when available, otherwise spiked (which also fills the
// cache).
func (o *Orch) seedOnboarding(ctx context.Context, p store.Project, repo store.Repo) {
	cache := o.onboardingCachePath(repo.Path)
	if b, err := os.ReadFile(cache); err == nil && strings.TrimSpace(string(b)) != "" {
		f, err := os.OpenFile(p.HandbookPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "\n%s\n", strings.TrimSpace(string(b)))
			f.Close()
			o.systemMessage(p.ID, "", "onboarding doc for "+repo.Name+" copied from cache (another project mapped it) — ask the EM to refresh_onboarding if it's stale")
			return
		}
		o.log.Error("append cached onboarding doc", "err", err)
	}
	if err := os.MkdirAll(o.d.Paths.OnboardingDir(), 0o755); err != nil {
		o.log.Error("onboarding dir", "err", err)
		return
	}
	if _, err := o.createTicket(ctx, p.ID, repo.Name, "spike",
		"Map local development workflow ("+repo.Name+")", onboardingBrief(p, repo, cache, false), ""); err != nil {
		o.log.Error("onboarding spike failed to open", "repo", repo.Name, "err", err)
	}
}

// refreshOnboarding re-spikes a repo's dev workflow, replacing both the
// cache and this project's handbook section.
func (o *Orch) refreshOnboarding(ctx context.Context, projectID, repoName string) (store.Ticket, error) {
	p, err := o.d.Store.ProjectByID(projectID)
	if err != nil {
		return store.Ticket{}, err
	}
	repos, err := o.d.Store.ReposForProject(projectID)
	if err != nil {
		return store.Ticket{}, err
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
		return store.Ticket{}, fmt.Errorf("unknown repo %q in project %s", repoName, p.Name)
	}
	if err := os.MkdirAll(o.d.Paths.OnboardingDir(), 0o755); err != nil {
		return store.Ticket{}, err
	}
	return o.createTicket(ctx, p.ID, repo.Name, "spike",
		"Refresh local development onboarding ("+repo.Name+")",
		onboardingBrief(p, repo, o.onboardingCachePath(repo.Path), true), "")
}
