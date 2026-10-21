package youtube

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestParseArgsFull(t *testing.T) {
	dir := t.TempDir()
	chapters := filepath.Join(dir, "chapters.json")
	if err := writeFile(chapters, `[{"at_ms":0,"title":"Intro"},{"at_ms":30000,"title":"Body"},{"at_ms":120000,"title":"Outro"}]`); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	req, opts, err := ParseArgs([]string{
		"--file", "/tmp/v.mp4",
		"--title", "Hello",
		"--description", "Desc",
		"--chapters-file", chapters,
		"--thumbnail", "/tmp/t.jpg",
		"--privacy", "private",
		"--publish-at", "2099-01-02T09:00:00-07:00",
		"--tags", " a, b ,,c ",
		"--category", "22",
		"--made-for-kids=true",
		"--dry-run",
	}, &stderr)
	if err != nil {
		t.Fatalf("ParseArgs: %v (stderr: %s)", err, stderr.String())
	}
	if !opts.DryRun {
		t.Error("dry-run not set")
	}
	if req.File != "/tmp/v.mp4" || req.Title != "Hello" || req.Description != "Desc" || req.Thumbnail != "/tmp/t.jpg" || req.CategoryID != "22" {
		t.Errorf("fields: %+v", req)
	}
	if !req.MadeForKids {
		t.Error("made-for-kids=true not honoured")
	}
	if !slices.Equal(req.Tags, []string{"a", "b", "c"}) {
		t.Errorf("tags %v", req.Tags)
	}
	if len(req.Chapters) != 3 || req.Chapters[2].Title != "Outro" {
		t.Errorf("chapters %+v", req.Chapters)
	}
	want := time.Date(2099, 1, 2, 16, 0, 0, 0, time.UTC)
	if !req.PublishAt.Equal(want) {
		t.Errorf("publish-at %s, want %s", req.PublishAt, want)
	}
	if req.EffectivePrivacy() != PrivacyPrivate {
		t.Errorf("effective privacy %q", req.EffectivePrivacy())
	}
}

func TestParseArgsDefaults(t *testing.T) {
	var stderr bytes.Buffer
	req, opts, err := ParseArgs([]string{"--file", "/tmp/v.mp4", "--title", "T"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if opts.DryRun || req.MadeForKids || req.Privacy != "" || req.Tags != nil || req.Chapters != nil || !req.PublishAt.IsZero() {
		t.Errorf("defaults: %+v %+v", req, opts)
	}
	if req.EffectivePrivacy() != PrivacyPrivate {
		t.Errorf("default privacy should be private, got %q", req.EffectivePrivacy())
	}
}

func TestParseArgsErrors(t *testing.T) {
	dir := t.TempDir()
	badChapters := filepath.Join(dir, "bad.json")
	if err := writeFile(badChapters, `[{"at_ms":0,"title":"only one"}]`); err != nil {
		t.Fatal(err)
	}
	base := []string{"--file", "/tmp/v.mp4", "--title", "T"}
	cases := []struct {
		name      string
		args      []string
		wantUsage bool
		wantErr   string
	}{
		{"unknown flag", append(slices.Clone(base), "--bogus"), true, "bogus"},
		{"positional", append(slices.Clone(base), "extra"), true, "positional"},
		{"missing title", []string{"--file", "/tmp/v.mp4"}, true, "--title is required"},
		{"missing file", []string{"--title", "T"}, true, "--file is required"},
		{"bad publish-at", append(slices.Clone(base), "--publish-at", "tomorrow"), true, "RFC3339"},
		{"past publish-at", append(slices.Clone(base), "--publish-at", "2001-01-01T00:00:00Z"), true, "not in the future"},
		{"publish-at with public", append(slices.Clone(base), "--publish-at", "2099-01-01T00:00:00Z", "--privacy", "public"), true, "requires --privacy private"},
		{"bad privacy", append(slices.Clone(base), "--privacy", "secret"), true, "--privacy must be"},
		{"chapters file missing", append(slices.Clone(base), "--chapters-file", filepath.Join(dir, "nope.json")), false, "chapters file"},
		{"chapters file invalid", append(slices.Clone(base), "--chapters-file", badChapters), false, "at least 3"},
	}
	for _, c := range cases {
		var stderr bytes.Buffer
		_, _, err := ParseArgs(c.args, &stderr)
		if err == nil {
			t.Errorf("%s: expected error", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: got %v, want error containing %q", c.name, err, c.wantErr)
		}
		var ue *UsageError
		if got := errors.As(err, &ue); got != c.wantUsage {
			t.Errorf("%s: usage error = %v, want %v (err %v)", c.name, got, c.wantUsage, err)
		}
	}
}

func TestParseArgsHelp(t *testing.T) {
	var stderr bytes.Buffer
	_, _, err := ParseArgs([]string{"-h"}, &stderr)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-h should return flag.ErrHelp, got %v", err)
	}
	if !strings.Contains(stderr.String(), "yt-upload") {
		t.Errorf("usage text missing: %s", stderr.String())
	}
}

func TestSplitTags(t *testing.T) {
	if got := SplitTags(""); got != nil {
		t.Errorf("empty -> %v", got)
	}
	if got := SplitTags(" , "); got != nil {
		t.Errorf("blank -> %v", got)
	}
	if got := SplitTags("a,b, c"); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("got %v", got)
	}
}
