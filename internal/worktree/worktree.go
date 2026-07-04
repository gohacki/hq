// Package worktree wraps the treehouse CLI: leased, isolated git worktrees
// for crewmates. treehouse owns pooling, cleanup safety, and state.
package worktree

import (
	"fmt"
	"os/exec"
	"strings"
)

// Lease acquires a durable worktree from repoPath's treehouse pool. The
// returned path stays ours until Return; treehouse never prunes leased trees.
func Lease(repoPath, holder string) (string, error) {
	cmd := exec.Command("treehouse", "get", "--lease", "--lease-holder", holder)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("treehouse get --lease in %s: %w %s", repoPath, err, msg)
	}
	// --lease prints only the worktree path on stdout (banners go to stderr).
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("treehouse get --lease returned no path")
	}
	return path, nil
}

// Return gives a worktree back to the pool. Callers must verify unlanded work
// is safe to drop before forcing.
func Return(worktreePath string) error {
	cmd := exec.Command("treehouse", "return", "--force", worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("treehouse return %s: %w %s", worktreePath, err, strings.TrimSpace(string(out)))
	}
	return nil
}
