package orch

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func repoWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")
	return dir
}

func TestUnlandedWork(t *testing.T) {
	// Clean, on a branch → safe.
	dir := repoWithCommit(t)
	if r := unlandedWork(dir); r != "" {
		t.Fatalf("clean branch should be safe, got %q", r)
	}

	// Uncommitted changes → kept.
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := unlandedWork(dir); r != "uncommitted changes" {
		t.Fatalf("dirty tree: got %q", r)
	}
	os.Remove(filepath.Join(dir, "b.txt"))

	// Detached HEAD at a branch-held commit → safe.
	gitRun(t, dir, "checkout", "--detach", "HEAD")
	if r := unlandedWork(dir); r != "" {
		t.Fatalf("detached at branch tip should be safe, got %q", r)
	}

	// Detached HEAD with a commit no branch contains → kept.
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "orphan work")
	if r := unlandedWork(dir); r == "" {
		t.Fatal("orphan detached commit must not be safe")
	}

	// Putting that commit on a branch makes it safe again.
	gitRun(t, dir, "branch", "rescue")
	if r := unlandedWork(dir); r != "" {
		t.Fatalf("commit on rescue branch should be safe, got %q", r)
	}
}
