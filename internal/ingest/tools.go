// Package ingest turns files dropped in a profile inbox into jobs (stage) and
// drives each job through the other longform subcommands by exec'ing the same
// binary (watch). It is pure transport: no judgment lives here.
package ingest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// toolDirs are searched after PATH so a launchd job with a minimal PATH still
// finds the Homebrew ffmpeg and the system brctl/osascript.
var toolDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"}

// LookPath finds an executable on PATH, then in toolDirs. It returns "" when
// the tool is absent so callers can degrade (brctl missing in tests, ffprobe
// missing on a bare machine).
func LookPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, d := range toolDirs {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// SelfExecutable is the binary the watcher execs for each job stage. Tests
// override it with LONGFORM_SELF so the exec order is observable.
func SelfExecutable() (string, error) {
	if v := os.Getenv("LONGFORM_SELF"); v != "" {
		return v, nil
	}
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// ErrNoFFprobe is returned when ffprobe cannot be found.
var ErrNoFFprobe = errors.New("ffprobe not found")

// ErrNoFFmpeg is returned when ffmpeg cannot be found.
var ErrNoFFmpeg = errors.New("ffmpeg not found")
