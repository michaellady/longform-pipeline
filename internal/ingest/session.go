package ingest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// ClaudeEnv overrides the claude binary (tests point it at a stub).
const ClaudeEnv = "LONGFORM_CLAUDE"

// ErrNotClipped is returned when launch-session runs before clips exist.
var ErrNotClipped = errors.New("job is not clipped yet")

// LaunchOptions tunes LaunchSession.
type LaunchOptions struct {
	Claude string           // path to claude; default LONGFORM_CLAUDE, PATH, ~/.local/bin/claude
	Force  bool             // relaunch even if already launched or not clipped
	Now    func() time.Time // default time.Now
}

// LaunchSession starts `claude -p "/longform-pipeline:longform-publish <jobdir>"` detached,
// with stdout at LogRoot()/<id>.session.json and stderr at <id>.session.err,
// and records pid and start time in job.extra. It returns false without
// launching when the job already has a session (idempotent).
func LaunchSession(j *model.Job, opts LaunchOptions) (bool, error) {
	if SessionLaunched(j) && !opts.Force {
		return false, nil
	}
	if !j.AtLeast(model.StateClipped) && !opts.Force {
		return false, fmt.Errorf("%w: state is %s", ErrNotClipped, j.State)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	claude := opts.Claude
	if claude == "" {
		claude = os.Getenv(ClaudeEnv)
	}
	if claude == "" {
		claude = LookPath("claude")
	}
	if claude == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if p := filepath.Join(home, ".local", "bin", "claude"); fileExists(p) {
				claude = p
			}
		}
	}
	if claude == "" {
		return false, errors.New("claude not found on PATH; set LONGFORM_CLAUDE")
	}
	if err := os.MkdirAll(model.LogRoot(), 0o755); err != nil {
		return false, err
	}
	outPath := filepath.Join(model.LogRoot(), j.ID+".session.json")
	errPath := filepath.Join(model.LogRoot(), j.ID+".session.err")
	out, err := os.OpenFile(outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer out.Close()
	errf, err := os.OpenFile(errPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer errf.Close()
	// A headless session cannot answer permission prompts, so the tools the
	// skill needs are allow-listed up front: the longform binary (by name and
	// by absolute path), the read/write tools for the job folder, and nothing
	// that publishes. Scheduling only ever happens in the approval phase.
	self, err := SelfExecutable()
	if err != nil || self == "" {
		self = "longform"
	}
	allowed := []string{
		"Bash(longform:*)",
		"Bash(" + self + ":*)",
		"Bash(ffmpeg:*)", "Bash(ffprobe:*)", "Bash(jq:*)", "Bash(ls:*)", "Bash(cat:*)",
		"Read", "Write", "Edit", "Glob", "Grep",
	}
	// Plugin skills are invoked with the plugin prefix (a bare /longform-pipeline:longform-publish
	// returns "Unknown command", as the first launchd run showed).
	cmd := exec.Command(claude, "-p", "/longform-pipeline:longform-publish "+j.Dir(),
		"--permission-mode", "acceptEdits",
		"--allowedTools", strings.Join(allowed, ","),
		"--output-format", "json")
	cmd.Dir = j.Dir()
	cmd.Stdout, cmd.Stderr = out, errf
	// The skill calls `longform …` by name: put this binary's directory first.
	cmd.Env = append([]string{"PATH=" + filepath.Dir(self) + ":" + os.Getenv("PATH")}, withoutPath(os.Environ())...)
	// Own session: launchd must not reap it with the watcher's process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start claude: %w", err)
	}
	if j.Extra == nil {
		j.Extra = map[string]any{}
	}
	j.Extra[SessionLaunchedKey] = opts.Now().UTC().Format(time.RFC3339)
	j.Extra["session_pid"] = cmd.Process.Pid
	j.Extra["session_stdout"] = outPath
	j.Extra["session_stderr"] = errPath
	if err := j.Save(); err != nil {
		return true, fmt.Errorf("session started (pid %d) but job.json not saved: %w", cmd.Process.Pid, err)
	}
	_ = cmd.Process.Release()
	return true, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// withoutPath drops PATH entries so a fresh PATH can be prepended.
func withoutPath(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
