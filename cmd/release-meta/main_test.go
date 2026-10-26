package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--bump", "huge"},
		{"--version", "0.3.0"},
		{"extra-arg"},
		{"--no-such-flag"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != exitUsage {
			t.Errorf("%v: exit %d, want %d (stderr %q)", args, code, exitUsage, errb.String())
		}
		if out.Len() != 0 {
			t.Errorf("%v: printed %q on stdout", args, out.String())
		}
	}
}

func TestNotesSubcommand(t *testing.T) {
	dir := t.TempDir()
	meta := filepath.Join(dir, "meta.json")
	sums := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(meta, []byte(`{"version":"v0.2.2","previous":"v0.2.1","bump":"patch","title":"t","summary":"It works.","release":true,"reason":"r","sha":"abc","prs":[19],"changes":["t (#19)"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sums, []byte("abc  longform_v0.2.2_darwin_arm64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"notes", "--meta", meta, "--checksums", sums, "--repo", "o/r"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{"## Summary\n\nIt works.\n", "### Changes since v0.2.1\n\n- t (#19)\n", "longform_v0.2.2_darwin_arm64.tar.gz\n```", "releases/download/v0.2.2/"} {
		if !strings.Contains(got, want) {
			t.Errorf("notes missing %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "## Summary") {
		t.Errorf("notes do not start with the summary:\n%s", got)
	}

	t.Setenv("GITHUB_REPOSITORY", "")
	if code := run([]string{"notes", "--meta", meta, "--checksums", sums}, &out, &errb); code != exitUsage {
		t.Errorf("no repo: exit %d, want %d", code, exitUsage)
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"version":"v0.2.2"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"notes", "--meta", empty, "--checksums", sums, "--repo", "o/r"}, &out, &errb); code != exitData {
		t.Errorf("meta without a summary: exit %d, want %d", code, exitData)
	}
}
