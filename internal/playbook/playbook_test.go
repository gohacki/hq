package playbook

import (
	"strings"
	"testing"
)

func TestRoundTripAndDefaults(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Fatal("empty dir should have no playbook")
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Recipe("web").EnvGlobsOrDefault(); len(got) == 0 || got[0] != ".env" {
		t.Fatalf("defaults = %v", got)
	}

	p.Prose = "# lifecycle\nintake → grill → build"
	p.Repos["web"] = RepoRecipe{EnvGlobs: []string{".env.local"}, Install: "npm ci", Verify: "npx tsc --noEmit", Dev: "npm run dev", PortNote: "PORT env var", Pipeline: "npm run lint && npm test"}
	if err := Save(dir, p); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Fatal("saved playbook not detected")
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prose != p.Prose || got.Recipe("web").Install != "npm ci" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if g := got.Recipe("web").EnvGlobsOrDefault(); g[0] != ".env.local" {
		t.Fatalf("explicit globs lost: %v", g)
	}
}

func TestBriefSection(t *testing.T) {
	p := Playbook{Prose: "the agreed lifecycle", Repos: map[string]RepoRecipe{
		"api": {Install: "npm ci", Verify: "npm test", Pipeline: "npm run lint"},
	}}
	s := BriefSection(p, []TreeInfo{
		{RepoName: "api", Path: "/x/tkt/api", Branch: "hq/1"},
		{RepoName: "web", Path: "/x/tkt/web", Branch: "hq/1"},
	})
	for _, want := range []string{"phase 0", "/x/tkt/api", "/x/tkt/web", "npm ci", "npm run lint", "the agreed lifecycle", "no recipe recorded"} {
		if !strings.Contains(s, want) {
			t.Fatalf("brief section missing %q:\n%s", want, s)
		}
	}
}
