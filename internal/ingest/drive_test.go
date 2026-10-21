package ingest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// stagedJob creates a job already in the staged state without touching ffmpeg.
func stagedJob(t *testing.T, profile, hash string, at time.Time) *model.Job {
	t.Helper()
	src := model.Source{ID: hash[:4], Path: "sources/01-x.mp4", SHA256: hash, RecordedAt: at}
	j, err := model.NewJob(profile, hash, []model.Source{src}, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Advance(model.StateStaged, "test", ""); err != nil {
		t.Fatal(err)
	}
	return j
}

func useHelper(t *testing.T) (string, *Driver) {
	t.Helper()
	calls := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("LONGFORM_TEST_HELPER", "1")
	t.Setenv("LONGFORM_TEST_CALLS", calls)
	t.Setenv("LONGFORM_TEST_WAIT_EXIT", "")
	t.Setenv("LONGFORM_SELF", os.Args[0])
	self, err := SelfExecutable()
	if err != nil || self != os.Args[0] {
		t.Fatalf("SelfExecutable = %q, %v", self, err)
	}
	return calls, &Driver{Self: self, Logf: t.Logf}
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func assertCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls:\n got %q\nwant %q", got, want)
	}
}

func TestDriveExecOrderAndLaunchOnce(t *testing.T) {
	setupEnv(t)
	calls, d := useHelper(t)
	j := stagedJob(t, "evc", strings.Repeat("a", 64), time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))

	jobs, _ := model.ListJobs()
	d.Drive(jobs)
	assertCalls(t, readCalls(t, calls), "prepare "+j.ID, "upload "+j.ID, "wait "+j.ID, "launch-session "+j.ID)

	re, err := model.LoadJob(j.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if re.State != model.StateClipped || !SessionLaunched(re) {
		t.Fatalf("state=%s launched=%v", re.State, SessionLaunched(re))
	}
	for _, step := range []string{"prepare", "upload", "wait", "launch-session"} {
		b, err := os.ReadFile(j.Path(filepath.Join("logs", step+".log")))
		if err != nil || !strings.Contains(string(b), "=== exit 0") {
			t.Errorf("log for %s: %v %q", step, err, b)
		}
	}

	// Second pass: clipped and launched, nothing runs.
	jobs, _ = model.ListJobs()
	d.Drive(jobs)
	if got := readCalls(t, calls); len(got) != 4 {
		t.Fatalf("second pass re-ran something: %q", got)
	}
}

func TestDriveStopsAtFailureAndKeepsOneInFlightPerProfile(t *testing.T) {
	setupEnv(t)
	calls, d := useHelper(t)
	a := stagedJob(t, "evc", strings.Repeat("a", 64), time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	b := stagedJob(t, "evc", strings.Repeat("b", 64), time.Date(2026, 10, 21, 9, 1, 0, 0, time.UTC))
	c := stagedJob(t, "bjj", strings.Repeat("c", 64), time.Date(2026, 10, 21, 9, 2, 0, 0, time.UTC))

	t.Setenv("LONGFORM_TEST_WAIT_EXIT", "75") // render not ready: wait exits 75
	jobs, _ := model.ListJobs()
	d.Drive(jobs)
	assertCalls(t, readCalls(t, calls),
		"prepare "+a.ID, "upload "+a.ID, "wait "+a.ID, // a stops at wait
		"prepare "+b.ID,                               // b must not upload while a is in flight
		"prepare "+c.ID, "upload "+c.ID, "wait "+c.ID) // other profile unaffected
	ra, _ := model.LoadJob(a.Dir())
	rb, _ := model.LoadJob(b.Dir())
	if ra.State != model.StateUploaded || rb.State != model.StatePrepared {
		t.Fatalf("a=%s b=%s", ra.State, rb.State)
	}

	t.Setenv("LONGFORM_TEST_WAIT_EXIT", "")
	os.Remove(calls)
	jobs, _ = model.ListJobs()
	d.Drive(jobs)
	assertCalls(t, readCalls(t, calls),
		"wait "+a.ID, "launch-session "+a.ID,
		"upload "+b.ID, "wait "+b.ID, "launch-session "+b.ID,
		"wait "+c.ID, "launch-session "+c.ID)
}

func TestDriveSkipsFailedAndUnexpectedStates(t *testing.T) {
	setupEnv(t)
	calls, d := useHelper(t)
	f := stagedJob(t, "evc", strings.Repeat("f", 64), time.Now())
	if err := f.Advance(model.StateFailed, "test", "boom"); err != nil {
		t.Fatal(err)
	}
	if _, err := model.NewJob("evc", strings.Repeat("d", 64), nil, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err) // stays detected: staging crashed midway
	}
	jobs, _ := model.ListJobs()
	d.Drive(jobs)
	if got := readCalls(t, calls); len(got) != 0 {
		t.Fatalf("nothing should run: %q", got)
	}
}

func TestPassStagesThenDrivesOnlyKnownProfiles(t *testing.T) {
	setupEnv(t)
	calls, d := useHelper(t)
	inbox := filepath.Join(t.TempDir(), "inbox")
	writeProfile(t, "evc", false, inbox)
	known := stagedJob(t, "evc", strings.Repeat("1", 64), time.Now())
	stagedJob(t, "orphan", strings.Repeat("2", 64), time.Now()) // no profile file: left alone
	opts := testOpts(t)
	if err := d.Pass("all", opts); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, readCalls(t, calls), "prepare "+known.ID, "upload "+known.ID, "wait "+known.ID, "launch-session "+known.ID)
	if !exists(inbox) {
		t.Error("pass should create a missing inbox so WatchPaths has a target")
	}
	if err := d.Pass("missing", opts); err != nil {
		t.Fatalf("unknown profile must be logged, not fatal: %v", err)
	}
}

func TestLaunchSessionDetachedAndOnce(t *testing.T) {
	setupEnv(t)
	stub, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no `true` binary")
	}
	t.Setenv(ClaudeEnv, stub)
	j := stagedJob(t, "evc", strings.Repeat("e", 64), time.Now())
	if _, err := LaunchSession(j, LaunchOptions{}); err == nil {
		t.Fatal("launch before clipped must fail")
	}
	for _, s := range []model.State{model.StatePrepared, model.StateUploaded, model.StateClipped} {
		if err := j.Advance(s, "test", ""); err != nil {
			t.Fatal(err)
		}
	}
	launched, err := LaunchSession(j, LaunchOptions{Now: func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }})
	if err != nil || !launched {
		t.Fatalf("launched=%v err=%v", launched, err)
	}
	re, _ := model.LoadJob(j.Dir())
	if v, _ := re.Extra[SessionLaunchedKey].(string); v != "2026-10-21T12:00:00Z" {
		t.Errorf("session_launched_at = %v", re.Extra[SessionLaunchedKey])
	}
	if pid, ok := re.Extra["session_pid"].(float64); !ok || pid <= 0 {
		t.Errorf("session_pid = %v", re.Extra["session_pid"])
	}
	if !exists(filepath.Join(model.LogRoot(), j.ID+".session.json")) || !exists(filepath.Join(model.LogRoot(), j.ID+".session.err")) {
		t.Error("session log files missing under LogRoot")
	}
	again, err := LaunchSession(re, LaunchOptions{})
	if err != nil || again {
		t.Fatalf("second launch: launched=%v err=%v", again, err)
	}
	forced, err := LaunchSession(re, LaunchOptions{Force: true})
	if err != nil || !forced {
		t.Fatalf("forced launch: launched=%v err=%v", forced, err)
	}
}
