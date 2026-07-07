package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=1"), 0o644)
	os.WriteFile(filepath.Join(dir, ".env.local"), []byte("LOCAL=1"), 0o644)
	run("add", "a.txt")
	run("commit", "-m", "init")
	return dir
}

func branches(t *testing.T, repo string) string {
	out, err := exec.Command("git", "-C", repo, "branch", "--format=%(refname:short)").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestCreateCopiesEnvAndSharesRefs(t *testing.T) {
	repo := gitRepo(t)
	root := t.TempDir()

	tree, err := Create(root, "tkt_abc123", repo, "main", []string{".env", ".env.*"})
	if err != nil {
		t.Fatal(err)
	}
	if tree.Branch != "hq/abc123" {
		t.Fatalf("branch = %q", tree.Branch)
	}
	// Env files copied, tracked file checked out.
	for _, f := range []string{"a.txt", ".env", ".env.local"} {
		if _, err := os.Stat(filepath.Join(tree.Path, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	// The branch is visible from the USER'S repo (shared refs).
	if !strings.Contains(branches(t, repo), "hq/abc123") {
		t.Fatalf("branch not in parent repo: %s", branches(t, repo))
	}

	// Commit in the worktree, remove it — the commit must survive in the repo.
	os.WriteFile(filepath.Join(tree.Path, "b.txt"), []byte("x"), 0o644)
	for _, args := range [][]string{{"add", "b.txt"}, {"commit", "-m", "work"}} {
		cmd := exec.Command("git", append([]string{"-C", tree.Path}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if err := Remove(repo, tree.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tree.Path); !os.IsNotExist(err) {
		t.Fatal("worktree dir still exists")
	}
	out, err := exec.Command("git", "-C", repo, "log", "--oneline", "hq/abc123").Output()
	if err != nil || !strings.Contains(string(out), "work") {
		t.Fatalf("branch commits lost after remove: %v %s", err, out)
	}
}

func TestCreateReattachesExistingBranch(t *testing.T) {
	repo := gitRepo(t)
	root := t.TempDir()
	tree, err := Create(root, "tkt_x1", repo, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Remove(repo, tree.Path); err != nil {
		t.Fatal(err)
	}
	// Branch still exists → a second Create must reuse it, not fail or reset.
	tree2, err := Create(root, "tkt_x1", repo, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tree2.Branch != "hq/x1" {
		t.Fatalf("branch = %q", tree2.Branch)
	}
}

func TestRemoveGoneDirIsFine(t *testing.T) {
	repo := gitRepo(t)
	root := t.TempDir()
	tree, err := Create(root, "tkt_gone", repo, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(tree.Path)
	if err := Remove(repo, tree.Path); err != nil {
		t.Fatalf("remove of vanished dir: %v", err)
	}
}
