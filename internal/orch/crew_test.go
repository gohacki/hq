package orch

import (
	"testing"
)

func TestProposedTasks(t *testing.T) {
	report := `# Findings

Stuff we learned.

## Proposed tasks

- Fix the login redirect: it drops the return-to param. Two sentence detail.
- Add rate limiting to /api/token: brute-forceable today.

## Appendix

- not a proposal, wrong section
`
	got := ProposedTasks(report)
	if len(got) != 2 {
		t.Fatalf("want 2 proposals, got %d: %v", len(got), got)
	}
	if got[0] != "Fix the login redirect: it drops the return-to param. Two sentence detail." {
		t.Fatalf("got[0] = %q", got[0])
	}
}

func TestProposedTasksEmpty(t *testing.T) {
	if got := ProposedTasks("# Findings\n\nnothing actionable\n"); len(got) != 0 {
		t.Fatalf("want none, got %v", got)
	}
	if got := ProposedTasks("## Proposed tasks\n\n(none)\n"); len(got) != 0 {
		t.Fatalf("want none, got %v", got)
	}
}

func TestTurnEndMarkers(t *testing.T) {
	cases := []struct {
		text                    string
		done, blocked, question bool
	}{
		{"All wrapped up.\nSTATUS: done — PR #12 opened", true, false, false},
		{"STATUS: blocked — no access to staging db", false, true, false},
		{"I checked both options.\nQUESTION: should retries be capped at 3 or 5?", false, false, true},
		{"just rambling with no marker", false, false, false},
		{"mentions status: done inline but not at line start", false, false, false},
	}
	for _, c := range cases {
		if got := reStatusDone.MatchString(c.text); got != c.done {
			t.Errorf("done(%q) = %v", c.text, got)
		}
		if got := reStatusBlocked.MatchString(c.text); got != c.blocked {
			t.Errorf("blocked(%q) = %v", c.text, got)
		}
		if got := reQuestion.MatchString(c.text); got != c.question {
			t.Errorf("question(%q) = %v", c.text, got)
		}
	}
}
