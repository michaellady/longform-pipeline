// Package media wraps ffmpeg and ffprobe for the prepare stage: probing,
// remuxing, HDR tonemapping, vidstab stabilisation, frame grabs, audio
// extraction and the censor render. Filtergraphs are built by pure functions
// (filters.go) so they are unit-testable without media.
package media

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Tools holds the resolved ffmpeg/ffprobe binaries and an optional log sink
// that receives every command line and its full stderr.
type Tools struct {
	FFmpeg  string
	FFprobe string
	Log     io.Writer
}

// FindTools resolves ffmpeg and ffprobe from PATH, falling back to the
// Homebrew prefix. It returns an error naming the missing binary.
func FindTools() (Tools, error) {
	var t Tools
	var err error
	if t.FFmpeg, err = findBinary("ffmpeg"); err != nil {
		return t, err
	}
	if t.FFprobe, err = findBinary("ffprobe"); err != nil {
		return t, err
	}
	return t, nil
}

func findBinary(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/" + name, "/usr/local/bin/" + name} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found on PATH or in /opt/homebrew/bin", name)
}

// ErrToolFailed wraps a non-zero exit from ffmpeg/ffprobe.
var ErrToolFailed = errors.New("media tool failed")

// run executes a tool with cwd=dir, logs the command and its stderr, and on
// failure returns the tail of stderr so callers get a readable cause.
func (t Tools) run(dir, bin string, args ...string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if t.Log != nil {
		fmt.Fprintf(t.Log, "$ %s %s\n", bin, strings.Join(args, " "))
	}
	err := cmd.Run()
	if t.Log != nil && stderr.Len() > 0 {
		t.Log.Write(stderr.Bytes())
		if !bytes.HasSuffix(stderr.Bytes(), []byte("\n")) {
			io.WriteString(t.Log, "\n")
		}
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%w: %s %s: %v\n%s", ErrToolFailed, bin, strings.Join(args, " "), err, tail(stderr.String(), 15))
	}
	return stdout.Bytes(), nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// HasFilter reports whether the local ffmpeg lists a filter by name.
func (t Tools) HasFilter(name string) (bool, error) {
	out, err := t.run("", t.FFmpeg, "-hide_banner", "-filters")
	if err != nil {
		return false, err
	}
	return ParseFilterList(out, name), nil
}

// ParseFilterList scans `ffmpeg -filters` output for a filter name. Lines look
// like " TS zscale           V->V       Apply resizing...".
func ParseFilterList(out []byte, name string) bool {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[1] == name {
			return true
		}
	}
	return false
}
