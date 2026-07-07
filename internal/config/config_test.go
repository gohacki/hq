package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	p := Paths{ConfigDir: dir, DataDir: filepath.Join(dir, "data")}

	// missing file → defaults
	c, err := p.LoadConfig()
	if err != nil || c.EMHarness != "claude" || c.EngHarness != "claude" {
		t.Fatalf("defaults: %+v %v", c, err)
	}

	// explicit config
	os.WriteFile(p.ConfigPath(), []byte(`{"em_harness":"pi"}`), 0o644)
	c, err = p.LoadConfig()
	if err != nil || c.EMHarness != "pi" || c.EngHarness != "claude" {
		t.Fatalf("partial config: %+v %v", c, err)
	}

	// env override wins
	t.Setenv("HQ_EM_HARNESS", "claude")
	if c, _ = p.LoadConfig(); c.EMHarness != "claude" {
		t.Fatalf("env override: %+v", c)
	}
	t.Setenv("HQ_EM_HARNESS", "")

	// bad harness name errors
	os.WriteFile(p.ConfigPath(), []byte(`{"em_harness":"gpt"}`), 0o644)
	if _, err = p.LoadConfig(); err == nil {
		t.Fatal("want error for unknown harness")
	}

	// malformed json errors
	os.WriteFile(p.ConfigPath(), []byte(`{oops`), 0o644)
	if _, err = p.LoadConfig(); err == nil {
		t.Fatal("want error for malformed config")
	}
}
