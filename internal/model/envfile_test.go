package model

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFileSetsMissingOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "env")
	// The wizard escapes a literal quote inside a single-quoted value as '\''.
	content := "# comment\n\nOPUSCLIP_API_KEY='abc'\\''def'\nexport SLACK_WEBHOOK_URL=\"https://hooks.example/x\"\nALREADY=fromfile\nbad line\n9BAD=x\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"OPUSCLIP_API_KEY", "SLACK_WEBHOOK_URL", "ALREADY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("ALREADY", "fromenv")

	set, err := loadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("OPUSCLIP_API_KEY"); got != "abc'def" {
		t.Errorf("single-quoted value with escaped quote: got %q", got)
	}
	if got := os.Getenv("SLACK_WEBHOOK_URL"); got != "https://hooks.example/x" {
		t.Errorf("double-quoted value: got %q", got)
	}
	if got := os.Getenv("ALREADY"); got != "fromenv" {
		t.Errorf("existing environment must win, got %q", got)
	}
	if len(set) != 2 {
		t.Errorf("want 2 names set, got %v", set)
	}
}

func TestLoadEnvFileMissingIsNotAnError(t *testing.T) {
	set, err := loadEnvFile(filepath.Join(t.TempDir(), "absent"))
	if err != nil || set != nil {
		t.Fatalf("missing file: set=%v err=%v", set, err)
	}
}

func TestEnvFilePathPrefersVariable(t *testing.T) {
	t.Setenv(EnvFileVar, "~/x/env")
	if got := EnvFilePath(); got != ExpandHome("~/x/env") {
		t.Errorf("got %q", got)
	}
	os.Unsetenv(EnvFileVar)
	t.Setenv("LONGFORM_CONFIG_DIR", "/cfg")
	if got := EnvFilePath(); got != "/cfg/env" {
		t.Errorf("default: got %q", got)
	}
}
