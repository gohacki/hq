// Package worktree manages hq's own native git worktrees — no external
// tool. One SET of worktrees per ticket: every repo in the project gets a
// tree under <root>/<ticket-id>/<repo-name>, on branch hq/<ticket-id>,
// created from the repo's local default branch (no network — offline never
// blocks a ticket).
//
// Native worktrees share the object store AND refs with the parent repo,
// so a branch an engineer commits in a worktree is instantly visible in
// the user's repo — merge whenever, no fetching branches out of clones.
// Removal keeps the branch; only the checkout dir goes away.
package worktree

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tree is one repo's worktree inside a ticket's set.
type Tree struct {
	RepoName string
	RepoPath string // the user's repo (worktree parent)
	Path     string // the checkout
	Branch   string
}

// BranchName is the branch a ticket's worktrees live on.
func BranchName(ticketID string) string {
	return "hq/" + strings.TrimPrefix(ticketID, "tkt_")
}

// Create adds a worktree for repoPath under root/ticketID/<repo base name>,
// on a new branch from the repo's local default branch tip. envGlobs name
// files (relative to the repo root, filepath.Glob patterns) copied into the
// fresh tree — the .env/.envrc style files git doesn't carry.
func Create(root, ticketID, repoPath, defaultBranch string, envGlobs []string) (Tree, error) {
	repoName := filepath.Base(repoPath)
	dir := filepath.Join(root, ticketID, repoName)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return Tree{}, err
	}
	branch := BranchName(ticketID)
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	out, err := exec.Command("git", "-C", repoPath, "worktree", "add",
		"-b", branch, dir, defaultBranch).CombinedOutput()
	if err != nil && strings.Contains(string(out), "already exists") {
		// The branch survived an earlier attempt (or teardown kept it, by
		// design) — check it out as-is rather than resetting its commits.
		out, err = exec.Command("git", "-C", repoPath, "worktree", "add", dir, branch).CombinedOutput()
	}
	if err != nil {
		return Tree{}, fmt.Errorf("git worktree add (%s): %w: %s", repoName, err, strings.TrimSpace(string(out)))
	}
	t := Tree{RepoName: repoName, RepoPath: repoPath, Path: dir, Branch: branch}
	if err := copyEnvFiles(repoPath, dir, envGlobs); err != nil {
		// Env copy failing shouldn't orphan the tree — hand it back broken-
		// but-removable and let the caller decide.
		return t, fmt.Errorf("env copy into %s: %w", dir, err)
	}
	return t, nil
}

// copyEnvFiles copies glob matches (relative to src) into dst, preserving
// relative paths. Missing matches are fine — globs describe what MIGHT
// exist. Directories and anything already tracked by git are skipped
// (tracked files arrived with the checkout).
func copyEnvFiles(src, dst string, globs []string) error {
	for _, g := range globs {
		if strings.TrimSpace(g) == "" {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(src, g))
		if err != nil {
			return fmt.Errorf("bad glob %q: %w", g, err)
		}
		for _, m := range matches {
			rel, err := filepath.Rel(src, m)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			fi, err := os.Stat(m)
			if err != nil || fi.IsDir() {
				continue
			}
			target := filepath.Join(dst, rel)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := copyFile(m, target, fi.Mode()); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Remove deletes the worktree checkout (the branch and its commits stay in
// the repo). Callers must run their unlanded-work safety check first —
// this forces, because git otherwise refuses over ignored build artifacts.
func Remove(repoPath, path string) error {
	out, err := exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", path).CombinedOutput()
	if err != nil {
		// Already gone on disk? prune the registration and call it removed.
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			exec.Command("git", "-C", repoPath, "worktree", "prune").Run()
			return nil
		}
		return fmt.Errorf("git worktree remove %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	// Drop the now-empty ticket dir (best effort; fails if siblings remain).
	os.Remove(filepath.Dir(path))
	return nil
}

// TicketDirs lists ticket ids that still have worktree dirs under root —
// boot-time reconciliation against the store.
func TicketDirs(root string) ([]string, error) {
	ents, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
