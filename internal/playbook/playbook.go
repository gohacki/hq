// Package playbook is a project's captured software-development lifecycle:
// a prose doc the humans (and agent briefs) read, plus a structured
// per-repo recipe the daemon executes deterministically. Both are written
// during the project setup interview (the EM's set_playbook tool) and live
// as plain files in the project's data dir — human-editable, no migration.
package playbook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RepoRecipe is what the machine needs to stand up and check one repo's
// worktree. Commands run with the worktree as cwd.
type RepoRecipe struct {
	// EnvGlobs name untracked files copied from the user's repo into every
	// fresh worktree (filepath.Glob, relative to the repo root).
	EnvGlobs []string `json:"env_globs,omitempty"`
	// Install readies dependencies (e.g. "npm ci").
	Install string `json:"install,omitempty"`
	// Verify proves the workspace works (e.g. "npx tsc --noEmit && npm test").
	Verify string `json:"verify,omitempty"`
	// Dev starts a local dev server; PORT-style placeholders per PortNote.
	Dev string `json:"dev,omitempty"`
	// PortNote says how to pick a unique port / avoid collisions across
	// parallel worktrees (env var, flag, config).
	PortNote string `json:"port_note,omitempty"`
	// Pipeline mirrors the CI pipeline locally (what must pass pre-push).
	Pipeline string `json:"pipeline,omitempty"`
}

type Playbook struct {
	// Prose is the lifecycle doc (markdown): intake → grill → plan → build →
	// pipeline → verify → deliver → merge → deploy, as agreed with the boss.
	Prose string            `json:"-"`
	Repos map[string]RepoRecipe `json:"repos"`
}

func jsonPath(projectDir string) string { return filepath.Join(projectDir, "playbook.json") }
func mdPath(projectDir string) string   { return filepath.Join(projectDir, "playbook.md") }

// DefaultEnvGlobs cover the common env-file shapes when no recipe exists yet.
var DefaultEnvGlobs = []string{".env", ".env.*", ".envrc"}

// Load reads a project's playbook; a missing playbook returns an empty one
// (Exists reports the difference).
func Load(projectDir string) (Playbook, error) {
	var p Playbook
	p.Repos = map[string]RepoRecipe{}
	if b, err := os.ReadFile(jsonPath(projectDir)); err == nil {
		if err := json.Unmarshal(b, &p); err != nil {
			return p, fmt.Errorf("playbook.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return p, err
	}
	if b, err := os.ReadFile(mdPath(projectDir)); err == nil {
		p.Prose = string(b)
	} else if !os.IsNotExist(err) {
		return p, err
	}
	return p, nil
}

// Exists reports whether the setup interview has produced a playbook yet.
func Exists(projectDir string) bool {
	_, errJ := os.Stat(jsonPath(projectDir))
	_, errM := os.Stat(mdPath(projectDir))
	return errJ == nil || errM == nil
}

// Save writes both halves atomically enough for single-writer use.
func Save(projectDir string, p Playbook) error {
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(struct {
		Repos map[string]RepoRecipe `json:"repos"`
	}{p.Repos}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath(projectDir), b, 0o644); err != nil {
		return err
	}
	return os.WriteFile(mdPath(projectDir), []byte(p.Prose), 0o644)
}

// Recipe returns repoName's recipe (zero value if unset).
func (p Playbook) Recipe(repoName string) RepoRecipe {
	return p.Repos[repoName]
}

// EnvGlobs returns the recipe's globs, or the defaults.
func (r RepoRecipe) EnvGlobsOrDefault() []string {
	if len(r.EnvGlobs) > 0 {
		return r.EnvGlobs
	}
	return DefaultEnvGlobs
}

// BriefSection renders the workspace-setup contract injected into an
// engineer's brief: the worktree set, the recipe per repo, and the
// mandatory phase-0 verify.
func BriefSection(p Playbook, trees []TreeInfo) string {
	var sb strings.Builder
	sb.WriteString("## Workspace (phase 0 — do this FIRST)\n\n")
	sb.WriteString("Your ticket owns one isolated git worktree PER project repo (same branch\nname in each). Primary = your cwd; siblings are ready beside it:\n\n")
	for _, t := range trees {
		sb.WriteString(fmt.Sprintf("- %s → %s (branch %s)\n", t.RepoName, t.Path, t.Branch))
	}
	sb.WriteString("\nEnv files were copied in deterministically. Before ANY ticket work, for\neach worktree you will actually touch:\n")
	for _, t := range trees {
		r := p.Recipe(t.RepoName)
		sb.WriteString(fmt.Sprintf("\n### %s\n", t.RepoName))
		if r.Install != "" {
			sb.WriteString(fmt.Sprintf("- install: `%s`\n", r.Install))
		} else {
			sb.WriteString("- install: (no recipe recorded — infer from the repo and note it in your report)\n")
		}
		if r.Verify != "" {
			sb.WriteString(fmt.Sprintf("- verify the workspace works: `%s`\n", r.Verify))
		}
		if r.Dev != "" {
			sb.WriteString(fmt.Sprintf("- dev server: `%s`", r.Dev))
			if r.PortNote != "" {
				sb.WriteString(" — ports: " + r.PortNote)
			}
			sb.WriteString("\n")
		}
		if r.Pipeline != "" {
			sb.WriteString(fmt.Sprintf("- pipeline mirror (must pass before demo/delivery): `%s`\n", r.Pipeline))
		}
	}
	sb.WriteString(`
If install/verify FAILS, fix the workspace first; if the playbook's recipe
itself is wrong, say exactly what was wrong in your report so the EM can
update it — the next 50 tickets depend on this recipe being right.
`)
	if strings.TrimSpace(p.Prose) != "" {
		sb.WriteString("\n## Project playbook (the agreed lifecycle)\n\n" + p.Prose + "\n")
	}
	return sb.String()
}

// TreeInfo mirrors worktree.Tree without importing it (keeps this package
// dependency-free for tests).
type TreeInfo struct {
	RepoName, Path, Branch string
}
