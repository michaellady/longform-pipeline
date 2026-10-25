package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

var fixtureDir string

// TestMain doubles as the stub longform binary: when LONGFORM_TEST_HELPER is
// set the test binary records the call and advances the job like the real
// stage command would, so Driver's exec order is observable in pure Go.
func TestMain(m *testing.M) {
	if os.Getenv("LONGFORM_TEST_HELPER") == "1" {
		os.Exit(helperMain(os.Args[1:]))
	}
	dir, err := os.MkdirTemp("", "longform-fixtures-*")
	if err != nil {
		panic(err)
	}
	fixtureDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func helperMain(args []string) int {
	if len(args) < 2 {
		return 64
	}
	// The job dir is the last argument; `manifest expire --job <dir>` is
	// recorded as "manifest expire".
	cmd, dir := args[0], args[len(args)-1]
	if cmd == "manifest" {
		cmd += " " + args[1]
	}
	if f := os.Getenv("LONGFORM_TEST_CALLS"); f != "" {
		fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return 70
		}
		fmt.Fprintf(fh, "%s %s\n", cmd, filepath.Base(dir))
		fh.Close()
	}
	j, err := model.LoadJob(dir)
	if err != nil {
		return 65
	}
	switch cmd {
	case "prepare":
		err = j.Advance(model.StatePrepared, cmd, "")
	case "upload":
		err = j.Advance(model.StateUploaded, cmd, "")
	case "wait":
		if code := os.Getenv("LONGFORM_TEST_WAIT_EXIT"); code != "" {
			n, _ := strconv.Atoi(code)
			return n
		}
		err = j.Advance(model.StateClipped, cmd, "")
	case "launch-session":
		if j.Extra == nil {
			j.Extra = map[string]any{}
		}
		j.Extra[SessionLaunchedKey] = time.Now().UTC().Format(time.RFC3339)
		err = j.Save()
	case "monitor":
		if code := os.Getenv("LONGFORM_TEST_MONITOR_EXIT"); code != "" {
			n, _ := strconv.Atoi(code)
			return n
		}
		err = j.Advance(model.StateMonitored, cmd, "")
	case "manifest expire":
	case "close":
		// Closes only when the test says the backlog is over; otherwise
		// exits 0 and leaves the state, like the real command does.
		if os.Getenv("LONGFORM_TEST_BACKLOG_OVER") == "1" {
			err = j.Advance(model.StateClosed, cmd, "")
		}
	default:
		return 64
	}
	if err != nil {
		return 70
	}
	return 0
}

func setupEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LONGFORM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("LONGFORM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("LONGFORM_LOG_DIR", filepath.Join(root, "logs"))
	t.Setenv("LONGFORM_SLACK_WEBHOOK", "")
	return root
}

func writeProfile(t *testing.T, name string, stitch bool, inbox string) *model.Profile {
	t.Helper()
	p := model.Profile{Name: name, Inbox: inbox, Orientation: "landscape", Stitch: model.StitchConfig{Enabled: stitch, SessionWindowMin: 90}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(model.ProfilesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model.ProfilePath(name), b, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := model.LoadProfile(name)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	p := LookPath("ffmpeg")
	if p == "" || LookPath("ffprobe") == "" {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	return p
}

// fixture renders a tiny test video once per name into fixtureDir.
func fixture(t *testing.T, name string, seconds int, creation string) string {
	t.Helper()
	ffmpeg := requireFFmpeg(t)
	out := filepath.Join(fixtureDir, name)
	if _, err := os.Stat(out); err == nil {
		return out
	}
	args := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=duration=%d:size=320x240:rate=30", seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=duration=%d", seconds),
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac"}
	if creation != "" {
		args = append(args, "-metadata", "creation_time="+creation)
	}
	args = append(args, out)
	if b, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture: %v\n%s", err, b)
	}
	return out
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func testOpts(t *testing.T) Options {
	return Options{StableSeconds: 0, PollInterval: 50 * time.Millisecond, Once: true, Logf: t.Logf,
		Notify: func(*model.Profile, *model.Job, string, string) {}}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestStageNewFileBecomesJobAndRerunIsNoop(t *testing.T) {
	setupEnv(t)
	inbox := filepath.Join(t.TempDir(), "inbox")
	p := writeProfile(t, "evc", false, inbox)
	fx := fixture(t, "single.mp4", 2, "")
	copyFile(t, fx, filepath.Join(inbox, "Take 1.MOV"))

	jobs, err := Stage(p, testOpts(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.State != model.StateStaged {
		t.Fatalf("state = %s, want staged", j.State)
	}
	if len(j.Sources) != 1 {
		t.Fatalf("sources = %d", len(j.Sources))
	}
	s := j.Sources[0]
	if s.Probe == nil {
		t.Fatal("probe missing")
	}
	if s.Probe.Width != 320 || s.Probe.Height != 240 || s.Probe.Codec != "h264" || s.Probe.AudioCodec != "aac" || s.Probe.FPS != "30" {
		t.Errorf("probe = %+v", *s.Probe)
	}
	if s.Probe.DurationMs < 1900 || s.Probe.DurationMs > 2200 {
		t.Errorf("duration = %d ms", s.Probe.DurationMs)
	}
	if s.Probe.HDR {
		t.Error("HDR should be false for a BT.709 fixture")
	}
	if s.Path != filepath.Join("sources", "01-Take 1.MOV") {
		t.Errorf("source path = %q", s.Path)
	}
	if exists(filepath.Join(inbox, "Take 1.MOV")) {
		t.Error("original still in inbox; expected a move")
	}
	if !exists(j.Path(s.Path)) {
		t.Error("source not in job sources/")
	}
	if j.Stitched || InputPath(j) != j.Path(s.Path) {
		t.Errorf("InputPath = %q", InputPath(j))
	}
	ix, err := model.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := ix.Has(s.SHA256); !ok || id != j.ID {
		t.Errorf("index missing source hash: %v %v", id, ok)
	}
	if id, ok := ix.Has(j.ContentHash); !ok || id != j.ID {
		t.Errorf("index missing content hash: %v %v", id, ok)
	}
	if j.ContentHash != ContentHash([]string{s.SHA256}) {
		t.Error("content hash is not sha256 over the sorted source hashes")
	}
	reloaded, err := model.LoadJob(j.Dir())
	if err != nil || reloaded.State != model.StateStaged {
		t.Fatalf("reload: %v state=%v", err, reloaded)
	}

	// Empty inbox: nothing happens.
	if jobs, err := Stage(p, testOpts(t)); err != nil || len(jobs) != 0 {
		t.Fatalf("second pass: jobs=%d err=%v", len(jobs), err)
	}
	// Same bytes again: duplicate by hash, no job, file left alone.
	copyFile(t, fx, filepath.Join(inbox, "again.mp4"))
	if jobs, err := Stage(p, testOpts(t)); err != nil || len(jobs) != 0 {
		t.Fatalf("duplicate pass: jobs=%d err=%v", len(jobs), err)
	}
	all, _ := model.ListJobs()
	if len(all) != 1 {
		t.Fatalf("jobs on disk = %d, want 1", len(all))
	}
	if !exists(filepath.Join(inbox, "again.mp4")) {
		t.Error("duplicate was removed from the inbox; it must only be skipped")
	}
}

func TestStageSkipsGrowingFileInOnceMode(t *testing.T) {
	if LookPath("ffprobe") == "" {
		t.Skip("ffprobe not installed: the settled-junk half of this test relies on ffprobe rejecting the file")
	}
	setupEnv(t)
	inbox := filepath.Join(t.TempDir(), "inbox")
	p := writeProfile(t, "evc", false, inbox)
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	grow := filepath.Join(inbox, "growing.mp4")
	f, err := os.OpenFile(grow, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer f.Close()
		chunk := make([]byte, 1024)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				f.Write(chunk)
			}
		}
	}()
	opts := testOpts(t)
	opts.StableSeconds = 1
	opts.PollInterval = 100 * time.Millisecond
	start := time.Now()
	jobs, err := Stage(p, opts)
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("growing file became a job: %v", jobs[0].ID)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("once mode should give up quickly, took %v", el)
	}
	if !exists(grow) {
		t.Error("growing file disappeared from the inbox")
	}
	if all, _ := model.ListJobs(); len(all) != 0 {
		t.Errorf("jobs on disk = %d", len(all))
	}
	// Once the file settles, the next pass takes it (it is not a real video, so
	// ffprobe rejects it and it stays in the inbox, which is the right outcome).
	jobs, err = Stage(p, opts)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("settled junk: jobs=%d err=%v", len(jobs), err)
	}
}

func TestStageStitchesSessionBatch(t *testing.T) {
	setupEnv(t)
	inbox := filepath.Join(t.TempDir(), "inbox")
	p := writeProfile(t, "bjj", true, inbox)
	first := fixture(t, "seg-1000.mp4", 2, "2026-10-21T10:00:00.000000Z")
	second := fixture(t, "seg-1030.mp4", 2, "2026-10-21T10:30:00.000000Z")
	later := fixture(t, "seg-1400.mp4", 2, "2026-10-21T14:00:00.000000Z")
	// Names sort against recording order so ordering must come from metadata.
	copyFile(t, first, filepath.Join(inbox, "zz-first.mp4"))
	copyFile(t, second, filepath.Join(inbox, "aa-second.mp4"))
	copyFile(t, later, filepath.Join(inbox, "mm-later.mov"))

	jobs, err := Stage(p, testOpts(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("want 2 jobs (one stitched session, one solo), got %d", len(jobs))
	}
	var stitched, solo *model.Job
	for _, j := range jobs {
		if j.Stitched {
			stitched = j
		} else {
			solo = j
		}
	}
	if stitched == nil || solo == nil {
		t.Fatalf("stitched=%v solo=%v", stitched, solo)
	}
	if len(stitched.Sources) != 2 || len(solo.Sources) != 1 {
		t.Fatalf("sources: stitched=%d solo=%d", len(stitched.Sources), len(solo.Sources))
	}
	if !strings.HasSuffix(stitched.Sources[0].Original, "zz-first.mp4") || !strings.HasSuffix(stitched.Sources[1].Original, "aa-second.mp4") {
		t.Errorf("sources not in recording order: %s, %s", stitched.Sources[0].Original, stitched.Sources[1].Original)
	}
	if stitched.Sources[0].Path != filepath.Join("sources", "01-zz-first.mp4") || stitched.Sources[1].Path != filepath.Join("sources", "02-aa-second.mp4") {
		t.Errorf("source paths: %s, %s", stitched.Sources[0].Path, stitched.Sources[1].Path)
	}
	if !stitched.Sources[0].RecordedAt.Equal(time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("recorded_at = %v", stitched.Sources[0].RecordedAt)
	}
	out := InputPath(stitched)
	if out != stitched.Path(StitchedName) || !exists(out) {
		t.Fatalf("stitched output missing: %s", out)
	}
	pr, err := Probe(out)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Probe.DurationMs < 3800 || pr.Probe.DurationMs > 4400 {
		t.Errorf("stitched duration = %d ms, want ~4000", pr.Probe.DurationMs)
	}
	if mode, _ := stitched.Extra["stitched_mode"].(string); mode != "copy" {
		t.Errorf("stitched_mode = %q, want copy for identical inputs", mode)
	}
	if stitched.State != model.StateStaged || solo.State != model.StateStaged {
		t.Errorf("states: %s %s", stitched.State, solo.State)
	}
	for _, s := range stitched.Sources {
		if !exists(stitched.Path(s.Path)) {
			t.Errorf("source %s not moved", s.Path)
		}
	}
	if !strings.HasSuffix(solo.Sources[0].Original, "mm-later.mov") {
		t.Errorf("solo source = %s", solo.Sources[0].Original)
	}
}

func TestStageExplicitFilesAndDeferralWhileBatchArrives(t *testing.T) {
	setupEnv(t)
	inbox := filepath.Join(t.TempDir(), "inbox")
	p := writeProfile(t, "bjj", true, inbox)
	fx := fixture(t, "explicit.mp4", 2, "")
	elsewhere := filepath.Join(t.TempDir(), "dry-run.mp4")
	copyFile(t, fx, elsewhere)
	opts := testOpts(t)
	opts.Files = []string{elsewhere, filepath.Join(t.TempDir(), "notes.txt")}
	jobs, err := Stage(p, opts)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("explicit file: jobs=%d err=%v", len(jobs), err)
	}
	if exists(elsewhere) {
		t.Error("explicit file should have been moved into the job")
	}

	// Stitch profile: a file still growing defers the whole pass.
	settled := fixture(t, "settled.mp4", 2, "2026-10-21T09:00:00.000000Z")
	copyFile(t, settled, filepath.Join(inbox, "settled.mp4"))
	grow := filepath.Join(inbox, "growing.mov")
	f, err := os.OpenFile(grow, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer f.Close()
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				f.Write([]byte("x"))
			}
		}
	}()
	opts = testOpts(t)
	opts.StableSeconds = 1
	opts.PollInterval = 100 * time.Millisecond
	jobs, err = Stage(p, opts)
	close(stop)
	<-done
	if err != nil || len(jobs) != 0 {
		t.Fatalf("deferral: jobs=%d err=%v", len(jobs), err)
	}
	if !exists(filepath.Join(inbox, "settled.mp4")) {
		t.Error("settled file was staged while the batch was still arriving")
	}
}

func TestGroupChainsWithinWindow(t *testing.T) {
	p := &model.Profile{Stitch: model.StitchConfig{Enabled: true, SessionWindowMin: 10}}
	at := func(min int) time.Time { return time.Date(2026, 1, 1, 0, min, 0, 0, time.UTC) }
	// a..b chain (9 min), b..d breaks (11 min), d..c chain (5 min).
	cands := []*candidate{{path: "c", recordedAt: at(25)}, {path: "a", recordedAt: at(0)}, {path: "b", recordedAt: at(9)}, {path: "d", recordedAt: at(20)}}
	g := group(p, cands)
	if len(g) != 2 || len(g[0]) != 2 || len(g[1]) != 2 {
		t.Fatalf("groups = %v", g)
	}
	if g[0][0].path != "a" || g[0][1].path != "b" || g[1][0].path != "d" || g[1][1].path != "c" {
		t.Errorf("order wrong: %s %s | %s %s", g[0][0].path, g[0][1].path, g[1][0].path, g[1][1].path)
	}
	p.Stitch.Enabled = false
	if g := group(p, cands); len(g) != 4 {
		t.Errorf("non-stitch profile should not group: %d", len(g))
	}
}

func TestParseProbe(t *testing.T) {
	raw := `{"streams":[{"codec_type":"video","codec_name":"hevc","width":1920,"height":1080,"r_frame_rate":"30000/1001","color_transfer":"arib-std-b67","side_data_list":[{"side_data_type":"Display Matrix","rotation":-90}]},{"codec_type":"audio","codec_name":"aac"}],"format":{"duration":"12.345","tags":{"creation_time":"2026-10-21T10:00:00.000000Z"}}}`
	pr, err := parseProbe([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := model.Probe{DurationMs: 12345, Width: 1920, Height: 1080, FPS: "29.97", Codec: "hevc", AudioCodec: "aac", ColorTransfer: "arib-std-b67", Rotation: -90, HDR: true}
	if pr.Probe != want {
		t.Errorf("probe = %+v, want %+v", pr.Probe, want)
	}
	if pr.CreationTime.IsZero() || !pr.HasAudio {
		t.Errorf("creation=%v audio=%v", pr.CreationTime, pr.HasAudio)
	}
}

func TestIsVideo(t *testing.T) {
	for _, n := range []string{"a.mov", "a.MOV", "a.mp4", "a.MP4", "a.m4v"} {
		if !IsVideo(n) {
			t.Errorf("%s should be a video", n)
		}
	}
	for _, n := range []string{"a.txt", "a.mp3", ".DS_Store", "a"} {
		if IsVideo(n) {
			t.Errorf("%s should not be a video", n)
		}
	}
}
