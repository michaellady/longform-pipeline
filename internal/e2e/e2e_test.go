// Package e2e drives one job through the real `longform` binary against the
// fake OpusClip server, from a dropped fixture file through a populated
// manifest.json, the seal, the watcher's monitor pass and backlog expiry to
// a closed job. It is gated by LONGFORM_E2E=1 because it builds the binary,
// shells out to ffmpeg, and takes tens of seconds.
//
//	LONGFORM_E2E=1 go test ./internal/e2e/ -run TestPipeline -v
//
// Optional knobs:
//
//	LONGFORM_E2E_KEEP=1          keep the temp dirs and print their paths
//	LONGFORM_WHISPER_MODEL=path  run real transcription instead of LONGFORM_SKIP_TRANSCRIBE=1
//
// The test codes against docs/cli-contract.md, not against any subcommand's
// implementation. Until every subcommand has merged it stops at the first one
// the binary does not know (exit 64 + "unknown command") and skips with a
// message naming it, so the harness is useful before and after the merge.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/fakeopus"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// harness holds everything one pipeline run needs.
type harness struct {
	t         *testing.T
	bin       string
	root      string // temp root
	configDir string
	dataDir   string
	logDir    string
	inbox     string
	fake      *fakeopus.Server
	apiURL    string
	env       []string
}

type result struct {
	args   []string
	stdout string
	stderr string
	code   int
	err    error
}

func (r result) String() string {
	return fmt.Sprintf("longform %s\nexit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s", strings.Join(r.args, " "), r.code, r.stdout, r.stderr)
}

// run executes the binary once. It never fails the test by itself; callers
// decide what an exit code means.
func (h *harness) run(args ...string) result {
	h.t.Helper()
	cmd := exec.Command(h.bin, args...)
	cmd.Env = h.env
	cmd.Dir = h.root
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	err := cmd.Run()
	r := result{args: args, stdout: out.String(), stderr: errb.String(), err: err}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		r.code = -1
	}
	h.t.Logf("$ longform %s -> exit %d (%s)", strings.Join(args, " "), r.code, time.Since(start).Round(10*time.Millisecond))
	return r
}

// mustOK runs and requires exit 0. An unknown subcommand skips the whole
// test (the binary predates the merge of that ticket) so the report says
// exactly where the pipeline stops today.
func (h *harness) mustOK(args ...string) result {
	h.t.Helper()
	r := h.run(args...)
	if r.code == 64 && strings.Contains(r.stderr, "unknown command") {
		h.t.Skipf("STOPPED at `longform %s`: the binary does not implement it yet (exit 64, unknown command).\n%s", strings.Join(args, " "), r)
	}
	if r.code != 0 {
		h.t.Fatalf("`longform %s` failed:\n%s", strings.Join(args, " "), r)
	}
	return r
}

func (h *harness) job(dir string) *model.Job {
	h.t.Helper()
	j, err := model.LoadJob(dir)
	if err != nil {
		h.t.Fatalf("load job %s: %v", dir, err)
	}
	return j
}

func (h *harness) wantState(dir string, want model.State) *model.Job {
	h.t.Helper()
	j := h.job(dir)
	if j.State != want {
		h.t.Fatalf("job %s: want state %q, got %q (error=%q)", j.ID, want, j.State, j.Error)
	}
	return j
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, b)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected artifact %s: %v", path, err)
	}
	if st.Size() == 0 {
		t.Fatalf("expected artifact %s to be non-empty", path)
	}
}

// cell mirrors the cells schema in the CLI contract.
type cell struct {
	Kind         string   `json:"kind"`
	ClipID       string   `json:"clip_id"`
	ProjectID    string   `json:"project_id"`
	Platform     string   `json:"platform"`
	AccountID    string   `json:"account_id"`
	SubAccountID string   `json:"sub_account_id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Hashtags     []string `json:"hashtags"`
	DurationSec  float64  `json:"duration_sec"`
	PublishAt    string   `json:"publish_at"`
	Label        string   `json:"label"`
}

// clipsFile mirrors clips.json in the CLI contract.
type clipsFile struct {
	ShortsProjectID string `json:"shorts_project_id"`
	FullProjectID   string `json:"full_project_id"`
	FullClipID      string `json:"full_clip_id"`
	Clips           []struct {
		ProjectID   string   `json:"project_id"`
		ClipID      string   `json:"clip_id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Hashtags    []string `json:"hashtags"`
		Score       float64  `json:"score"`
		DurationSec float64  `json:"duration_sec"`
		URIPreview  string   `json:"uri_preview"`
		URIExport   string   `json:"uri_export"`
		IsBonus     bool     `json:"is_bonus"`
	} `json:"clips"`
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if os.Getenv("LONGFORM_E2E") != "1" {
		t.Skip("set LONGFORM_E2E=1 to run the end-to-end pipeline test (builds the binary, needs ffmpeg)")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH; the fixture cannot be generated (brew install ffmpeg)")
	}

	var root string
	if os.Getenv("LONGFORM_E2E_KEEP") == "1" {
		var err error
		root, err = os.MkdirTemp("", "longform-e2e-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("LONGFORM_E2E_KEEP=1: artifacts stay under %s", root)
	} else {
		root = t.TempDir()
	}
	h := &harness{t: t, root: root}
	h.configDir = filepath.Join(root, "config")
	h.dataDir = filepath.Join(root, "data")
	h.logDir = filepath.Join(root, "logs")
	h.inbox = filepath.Join(root, "inbox")
	for _, d := range []string{h.configDir, h.dataDir, h.logDir, h.inbox} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Build the real binary from this checkout.
	_, thisFile, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	h.bin = filepath.Join(root, "longform")
	build := exec.Command("go", "build", "-o", h.bin, "./cmd/longform")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/longform failed: %v\n%s", err, out)
	}

	// Fake OpusClip.
	h.fake = fakeopus.New(fakeopus.Options{
		APIKey:             "test",
		ProcessingDelay:    2 * time.Second,
		DefaultDurationSec: 5,
		Logf:               func(f string, a ...any) { t.Logf(f, a...) },
	})
	url, err := h.fake.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.apiURL = url
	t.Cleanup(func() { _ = h.fake.Close() })

	// The test process and the child both see the sandbox roots, so
	// internal/model helpers (ListJobs, ProfilePath) resolve inside it.
	t.Setenv("LONGFORM_CONFIG_DIR", h.configDir)
	t.Setenv("LONGFORM_DATA_DIR", h.dataDir)
	t.Setenv("LONGFORM_LOG_DIR", h.logDir)
	h.env = append(os.Environ(),
		"LONGFORM_CONFIG_DIR="+h.configDir,
		"LONGFORM_DATA_DIR="+h.dataDir,
		"LONGFORM_LOG_DIR="+h.logDir,
		"OPUSCLIP_API_URL="+h.apiURL,
		"OPUSCLIP_API_KEY=test",
		"HOME="+root, // keep any stray ~ expansion inside the sandbox
		"PATH="+os.Getenv("PATH")+":/opt/homebrew/bin",
	)
	// `watch --once` launches the editorial session for a job that never had
	// one; point it at a no-op so the test never starts a real claude.
	if stub, err := exec.LookPath("true"); err == nil {
		h.env = append(h.env, "LONGFORM_CLAUDE="+stub)
	} else {
		t.Skip("no `true` binary to stub claude with")
	}
	if os.Getenv("LONGFORM_WHISPER_MODEL") == "" {
		h.env = append(h.env, "LONGFORM_SKIP_TRANSCRIBE=1")
		t.Log("LONGFORM_WHISPER_MODEL unset: running prepare with LONGFORM_SKIP_TRANSCRIBE=1 (transcript will be empty)")
	}
	return h
}

func (h *harness) writeProfileAndCensor() {
	t := h.t
	p := model.Profile{
		Name:        "evc",
		Brand:       model.Brand{Display: "EVC Test", Site: "https://example.test"},
		Inbox:       h.inbox,
		Orientation: "portrait",
		Stitch:      model.StitchConfig{Enabled: false},
		Stabilize:   model.StabilizeConfig{Enabled: false},
		Censor:      model.CensorConfig{Enabled: true, Mode: "beep", Extra: []string{"heck"}},
		Transcribe:  model.TranscribeConfig{Language: "en", Threads: 4},
		OpusClip: model.OpusClipConfig{
			StripOverlay: false,
			Shorts:       model.CurationConfig{Model: "ClipBasic", Durations: []int{30, 60, 90}, Aspect: "portrait", RemoveFiller: false},
			Full:         model.FullRenderConfig{SkipCurate: true, Aspect: "portrait"},
			Accounts:     map[string]string{"youtube": "acc-yt", "instagram": "acc-ig"},
			SubAccounts:  map[string]string{"instagram": "ig-sub-1"},
		},
		Cadence:     map[string]int{"youtube": 2, "instagram": 1},
		SlotsLocal:  []string{"09:00", "12:00", "15:00", "18:00", "21:00"},
		Timezone:    "America/Los_Angeles",
		BacklogDays: 25,
		CTA: model.CTAConfig{
			LeadMagnets: map[string]string{"guide": "https://example.test/guide"},
			CommentToDM: map[string]string{"instagram": "GUIDE"},
		},
		Copy:    model.CopyConfig{Style: "plain"},
		Buffer:  model.BufferConfig{Channels: map[string]string{"instagram": "buf-ig"}, Tags: map[string]string{}},
		Statics: model.StaticsConfig{TemplatesDir: filepath.Join(h.configDir, "templates"), Formats: []string{}},
		Notify:  model.NotifyConfig{MacOS: false},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("test profile is invalid: %v", err)
	}
	writeJSON(t, model.ProfilePath("evc"), p)
	writeJSON(t, filepath.Join(h.configDir, "censor", "default.json"), map[string]any{
		"schema_version": "1",
		"words":          []string{"damn", "crap"},
	})
}

// makeFixture renders a 5-second 1080x1920 test pattern with a sine tone.
func (h *harness) makeFixture() string {
	t := h.t
	out := filepath.Join(h.inbox, "IMG_0001.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=1080x1920:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "5", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", "-movflags", "+faststart", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture failed: %v\n%s", err, b)
	}
	mustExist(t, out)
	return out
}

func TestPipeline(t *testing.T) {
	h := newHarness(t)
	h.writeProfileAndCensor()
	fixture := h.makeFixture()

	// Sanity: the scaffold commands exist before anything else.
	h.mustOK("version")

	// --- stage -------------------------------------------------------------
	h.mustOK("stage", "--profile", "evc", "--file", fixture)
	jobs, err := model.ListJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("after stage: want 1 job under %s, got %d", h.dataDir, len(jobs))
	}
	jobDir := jobs[0].Dir()
	j := h.wantState(jobDir, model.StateStaged)
	for _, sub := range []string{"sources", "analysis", "review", "statics", "logs"} {
		if st, err := os.Stat(filepath.Join(jobDir, sub)); err != nil || !st.IsDir() {
			t.Fatalf("job dir missing %s/", sub)
		}
	}
	if len(j.Sources) != 1 || j.Sources[0].SHA256 == "" {
		t.Fatalf("staged job should record one source with a sha256: %+v", j.Sources)
	}
	// Idempotent: a second pass over the same file must not create a second job.
	h.mustOK("stage", "--profile", "evc", "--file", fixture)
	if again, _ := model.ListJobs(); len(again) != 1 {
		t.Fatalf("second stage of the same file created a duplicate job (%d jobs)", len(again))
	}

	// --- prepare -----------------------------------------------------------
	h.mustOK("prepare", jobDir)
	h.wantState(jobDir, model.StatePrepared)
	mustExist(t, filepath.Join(jobDir, "delivery.mp4"))
	mustExist(t, filepath.Join(jobDir, "analysis", "transcript.json"))
	mustExist(t, filepath.Join(jobDir, "analysis", "censored.json"))
	h.mustOK("prepare", jobDir) // idempotent
	h.wantState(jobDir, model.StatePrepared)

	// --- upload ------------------------------------------------------------
	h.mustOK("upload", jobDir)
	j = h.wantState(jobDir, model.StateUploaded)
	if j.OpusClip.ShortsProjectID == "" || j.OpusClip.FullProjectID == "" {
		t.Fatalf("uploaded job must record both project ids: %+v", j.OpusClip)
	}
	posts := h.fake.RequestsMatching("POST", "/api/clip-projects")
	if len(posts) != 2 {
		t.Fatalf("want exactly 2 POST /clip-projects, got %d:\n%s", len(posts), dumpRequests(posts))
	}
	var sawShorts, sawFull bool
	for _, r := range posts {
		if strings.Contains(r.Body, "clipDurations") {
			sawShorts = true
		}
		if strings.Contains(r.Body, `"skipCurate":true`) || strings.Contains(r.Body, `"skipCurate": true`) {
			sawFull = true
		}
	}
	if !sawShorts || !sawFull {
		t.Fatalf("expected one shorts project (curationPref.clipDurations) and one full project (curationPref.skipCurate true):\n%s", dumpRequests(posts))
	}
	if n := len(h.fake.RequestsMatching("PUT", "/gcs/session/")); n != 1 {
		t.Fatalf("want exactly one PUT of the delivery bytes, got %d", n)
	}
	h.mustOK("upload", jobDir) // idempotent: no second submit
	if n := len(h.fake.RequestsMatching("POST", "/api/clip-projects")); n != 2 {
		t.Fatalf("re-running upload re-submitted projects (%d POST /clip-projects)", n)
	}

	// --- wait --------------------------------------------------------------
	h.mustOK("wait", jobDir, "--interval", "1s", "--ceiling", "30s")
	j = h.wantState(jobDir, model.StateClipped)
	var clips clipsFile
	readJSON(t, filepath.Join(jobDir, "clips.json"), &clips)
	if clips.ShortsProjectID != j.OpusClip.ShortsProjectID || clips.FullProjectID != j.OpusClip.FullProjectID {
		t.Fatalf("clips.json project ids %q/%q do not match job.json %+v", clips.ShortsProjectID, clips.FullProjectID, j.OpusClip)
	}
	if clips.FullClipID == "" {
		t.Fatalf("clips.json must carry full_clip_id")
	}
	// clips[] carries the shorts project's clips plus the full render (the one
	// whose project_id is the full project and whose clip_id is full_clip_id),
	// which is how the live run on 2026-10-21 wrote it.
	shorts, full, bonus := 0, 0, 0
	for _, c := range clips.Clips {
		switch {
		case c.ClipID == "":
			t.Fatalf("clip without id: %+v", c)
		case c.ProjectID == clips.ShortsProjectID:
			shorts++
			if c.IsBonus {
				bonus++
			}
		case c.ProjectID == clips.FullProjectID && c.ClipID == clips.FullClipID:
			full++
		default:
			t.Fatalf("clip with unexpected project: %+v", c)
		}
	}
	if shorts != 3 || full != 1 {
		t.Fatalf("want the fake's 3 shorts clips and 1 full render in clips.json, got shorts=%d full=%d: %+v", shorts, full, clips.Clips)
	}
	if bonus != 1 {
		t.Fatalf("want exactly one is_bonus clip, got %d", bonus)
	}
	if n := len(h.fake.RequestsMatching("GET", "/api/exportable-clips")); n == 0 {
		t.Fatalf("wait never polled /exportable-clips")
	}

	// --- draft.json (what the longform-publish skill would write) ----------
	type draftCell struct {
		Platform     string   `json:"platform"`
		AccountID    string   `json:"account_id"`
		SubAccountID string   `json:"sub_account_id"`
		Title        string   `json:"title"`
		Description  string   `json:"description"`
		Hashtags     []string `json:"hashtags"`
		PublishAt    string   `json:"publish_at"`
	}
	type draftClip struct {
		ProjectID   string      `json:"project_id"`
		ClipID      string      `json:"clip_id"`
		Score       float64     `json:"score"`
		DurationSec float64     `json:"duration_sec"`
		ShortTitle  string      `json:"short_title"`
		Status      string      `json:"status"`
		PreviewURL  string      `json:"preview_url"`
		Cells       []draftCell `json:"cells"`
	}
	var dclips []draftClip
	wantCells := 0
	for i, c := range clips.Clips {
		dc := draftClip{ProjectID: c.ProjectID, ClipID: c.ClipID, Score: c.Score, DurationSec: c.DurationSec, ShortTitle: c.Title, PreviewURL: c.URIPreview, Status: "held"}
		if c.Score >= 70 {
			dc.Status = "post"
			dc.Cells = []draftCell{
				{Platform: "youtube", AccountID: "acc-yt", Title: fmt.Sprintf("Short %d: %s", i+1, c.Title), Description: "Hook. CTA: https://example.test/guide?utm_source=youtube&utm_medium=short", Hashtags: []string{"#evc", "#shorts"}},
			}
			if i == 0 {
				dc.Cells = append(dc.Cells, draftCell{Platform: "instagram", AccountID: "acc-ig", SubAccountID: "ig-sub-1", Title: c.Title, Description: "Comment GUIDE and I will DM you the link.", Hashtags: []string{"#evc"}})
			}
			wantCells += len(dc.Cells)
		}
		dclips = append(dclips, dc)
	}
	wantCells++ // the full video on youtube
	wantCells++ // the quote static on instagram (Buffer)
	draft := map[string]any{
		"schema_version": "1",
		"job_id":         j.ID,
		"profile":        "evc",
		"full_video": map[string]any{
			"project_id":  clips.FullProjectID,
			"clip_id":     clips.FullClipID,
			"title":       "E2E fixture: a five second walk",
			"description": "Full video. https://example.test/guide?utm_source=youtube&utm_medium=longform",
			"chapters":    []map[string]any{{"at_ms": 0, "title": "Intro"}, {"at_ms": 2500, "title": "The point"}},
			"thumbnails":  []map[string]any{},
			"platforms":   []string{"youtube"},
		},
		"clips": dclips,
		"statics": []map[string]any{{
			"id": "s1", "format": "quote", "platforms": []string{"instagram"},
			"caption": "The one habit that fixed my mornings. Comment GUIDE for the link.",
			"inputs":  map[string]any{"quote": "Fix the morning, fix the day.", "attribution": "EVC Test"},
		}},
		"censored":      []any{},
		"stabilization": map[string]any{"before": "", "after": ""},
		"credits":       map[string]any{"spent": 2, "to_spend": 0},
		"decisions":     []any{},
	}
	writeJSON(t, filepath.Join(jobDir, "review", "draft.json"), draft)

	// --- brief render / parse-decision ------------------------------------
	h.mustOK("brief", "render", "--job", jobDir)
	h.wantState(jobDir, model.StateDrafted)
	mustExist(t, filepath.Join(jobDir, "review", "brief.md"))

	h.mustOK("brief", "parse-decision", "--job", jobDir, "approve")
	h.wantState(jobDir, model.StateApproved)
	mustExist(t, filepath.Join(jobDir, "review", "decision.json"))
	cellsPath := filepath.Join(jobDir, "review", "schedule.json")
	// brief parse-decision writes {schema_version, job_id, cells: [...]}; the
	// downstream commands accept that object or a bare array.
	var schedule struct {
		Cells []cell `json:"cells"`
	}
	readJSON(t, cellsPath, &schedule)
	cells := schedule.Cells
	if len(cells) != wantCells {
		t.Fatalf("schedule.json: want %d cells (one per posted clip cell + the full video + the static), got %d:\n%s", wantCells, len(cells), mustPretty(cells))
	}

	// --- slots propose -----------------------------------------------------
	existing := filepath.Join(h.root, "existing.json")
	writeJSON(t, existing, []any{})
	r := h.mustOK("slots", "propose", "--profile", "evc", "--cells", cellsPath, "--existing", existing)
	// The contract says "fills publish_at"; accept either stdout JSON or an in-place rewrite.
	var proposed []cell
	if strings.HasPrefix(strings.TrimSpace(r.stdout), "[") && json.Unmarshal([]byte(r.stdout), &proposed) == nil && len(proposed) > 0 {
		writeJSON(t, cellsPath, proposed)
	}
	readJSON(t, cellsPath, &cells)
	for i, c := range cells {
		if c.PublishAt == "" {
			t.Fatalf("cell %d has no publish_at after slots propose:\n%s", i, r)
		}
		if _, err := time.Parse(time.RFC3339, c.PublishAt); err != nil {
			t.Fatalf("cell %d publish_at %q is not RFC3339: %v", i, c.PublishAt, err)
		}
	}

	// --- gate validate -----------------------------------------------------
	h.mustOK("gate", "validate", "--profile", "evc", "--cells", cellsPath)

	// --- opus schedule -----------------------------------------------------
	// --job as the skill passes it. No manifest.json yet, so every OpusClip
	// cell posts and opus schedule records each post itself; the static is
	// left for Buffer (step 6).
	nOpus := 0
	var static cell
	for _, c := range cells {
		if c.Kind == "static" {
			static = c
		} else {
			nOpus++
		}
	}
	if static.ClipID == "" || nOpus != 4 {
		t.Fatalf("schedule.json should hold 4 OpusClip cells and one static:\n%s", mustPretty(cells))
	}
	before := len(h.fake.RequestsMatching("POST", "/api/publish-schedules"))
	r = h.mustOK("opus", "schedule", "--cells", cellsPath, "--job", jobDir)
	for _, want := range []string{fmt.Sprintf("schedule: %d scheduled, 1 skipped", nOpus), "schedule: static " + static.ClipID + " goes through Buffer, skipped"} {
		if !strings.Contains(r.stderr, want) {
			t.Fatalf("opus schedule stderr lacks %q:\n%s", want, r)
		}
	}
	resultsPath := filepath.Join(h.root, "schedule-results.jsonl")
	if err := os.WriteFile(resultsPath, []byte(r.stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := nonEmptyLines(r.stdout)
	if len(lines) != nOpus {
		t.Fatalf("opus schedule: want %d JSON lines (one per OpusClip cell), got %d:\n%s", nOpus, len(lines), r)
	}
	for i, ln := range lines {
		var rec struct {
			Cell     json.RawMessage `json:"cell"`
			Response struct {
				Data struct {
					ScheduleID string `json:"scheduleId"`
				} `json:"data"`
			} `json:"response"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("opus schedule line %d is not JSON: %v\n%s", i, err, ln)
		}
		if rec.Error != "" || rec.Response.Data.ScheduleID == "" {
			t.Fatalf("opus schedule line %d: no scheduleId (error=%q):\n%s", i, rec.Error, ln)
		}
	}
	scheds := h.fake.RequestsMatching("POST", "/api/publish-schedules")[before:]
	if len(scheds) != nOpus {
		t.Fatalf("fake saw %d POST /publish-schedules, want %d", len(scheds), nOpus)
	}
	if len(scheds) >= 2 {
		gap := scheds[len(scheds)-1].At.Sub(scheds[0].At)
		if minGap := time.Duration(len(scheds)-1) * 900 * time.Millisecond; gap < minGap {
			t.Fatalf("opus schedule is not paced at 1 req/s: %d calls in %s", len(scheds), gap)
		}
	}

	// --- manifest ----------------------------------------------------------
	// opus schedule --job already recorded every post, right after each
	// POST. The manifest keeps the vendored shape: scheduled_posts nest
	// under each clip so content-attribution and pm-tool keep reading it.
	manifestPath := filepath.Join(jobDir, "manifest.json")
	var manifest struct{ ScheduledPosts []map[string]any }
	readPosts := func() {
		var doc struct {
			Clips []struct {
				ScheduledPosts []map[string]any `json:"scheduled_posts"`
			} `json:"clips"`
		}
		readJSON(t, manifestPath, &doc)
		manifest.ScheduledPosts = nil
		for _, c := range doc.Clips {
			manifest.ScheduledPosts = append(manifest.ScheduledPosts, c.ScheduledPosts...)
		}
	}
	readPosts()
	if len(manifest.ScheduledPosts) != nOpus {
		b, _ := os.ReadFile(manifestPath)
		t.Fatalf("right after opus schedule --job, manifest.json: want %d scheduled_posts across clips, got %d:\n%s", nOpus, len(manifest.ScheduledPosts), b)
	}
	for i, p := range manifest.ScheduledPosts {
		id, _ := p["schedule_id"].(string)
		if id == "" {
			t.Fatalf("manifest scheduled_posts[%d] has no schedule_id: %v", i, p)
		}
		if _, ok := h.fake.Schedules()[id]; !ok {
			t.Fatalf("manifest scheduled_posts[%d].schedule_id %q was never issued by the fake", i, id)
		}
		if l, _ := p["label"].(string); l == "" {
			t.Fatalf("manifest scheduled_posts[%d] has no label: %v", i, p)
		}
	}
	// init is idempotent, and the skill's backwards-compatible pipe of the
	// lines into append-post only finds duplicates.
	h.mustOK("manifest", "init", "--job", jobDir)
	r = h.mustOK("manifest", "append-post", "--job", jobDir, "--file", resultsPath)
	if n := strings.Count(r.stderr, "append-post: duplicate "); n != nOpus || !strings.Contains(r.stderr, "appended 0 posts") {
		t.Fatalf("append-post after opus schedule --job: want %d duplicate lines and nothing appended:\n%s", nOpus, r)
	}

	// --- the static through Buffer (step 6) ----------------------------------
	// The skill posts it with the Buffer MCP and appends the reply with the
	// post id it names.
	bufferLine, _ := json.Marshal(map[string]any{"cell": static, "response": map[string]any{"post": map[string]any{"id": "buf-e2e-1"}}, "transport": "buffer", "post_id": "buf-e2e-1"})
	bufferPath := filepath.Join(h.root, "buffer-lines.jsonl")
	if err := os.WriteFile(bufferPath, append(bufferLine, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	r = h.mustOK("manifest", "append-post", "--job", jobDir, "--file", bufferPath)
	if !strings.Contains(r.stderr, fmt.Sprintf("appended 1 posts (%d total)", nOpus+1)) {
		t.Fatalf("buffer append:\n%s", r)
	}

	// --- re-run after a crash: nothing posts twice --------------------------
	// The approval phase re-run from step 5: every cell is in the manifest,
	// so opus schedule --job posts nothing and prints nothing on stdout, and
	// re-appending the first run's lines adds nothing.
	nSched := len(h.fake.Schedules())
	before = len(h.fake.RequestsMatching("POST", "/api/publish-schedules"))
	r = h.mustOK("opus", "schedule", "--cells", cellsPath, "--job", jobDir)
	if strings.TrimSpace(r.stdout) != "" {
		t.Fatalf("second opus schedule printed on stdout (it is piped into append-post):\n%s", r)
	}
	if n := len(h.fake.RequestsMatching("POST", "/api/publish-schedules")) - before; n != 0 || len(h.fake.Schedules()) != nSched {
		t.Fatalf("second opus schedule made %d POST /publish-schedules (fake holds %d schedules, had %d)", n, len(h.fake.Schedules()), nSched)
	}
	if want := fmt.Sprintf("schedule: 0 scheduled, %d skipped", len(cells)); !strings.Contains(r.stderr, want) {
		t.Fatalf("second opus schedule stderr lacks %q:\n%s", want, r)
	}
	if n := strings.Count(r.stderr, ": already in manifest ("); n != nOpus || !strings.Contains(r.stderr, "schedule: static "+static.ClipID+" goes through Buffer, skipped") {
		t.Fatalf("second opus schedule: want one skip line per OpusClip cell (%d) and the static's, got %d:\n%s", nOpus, n, r)
	}
	r = h.mustOK("manifest", "append-post", "--job", jobDir, "--file", resultsPath)
	if n := strings.Count(r.stderr, "append-post: duplicate "); n != nOpus {
		t.Fatalf("re-append: want %d duplicate lines, got %d:\n%s", nOpus, n, r)
	}
	if got := strings.TrimSpace(h.mustOK("manifest", "count-scheduled", "--job", jobDir).stdout); got != fmt.Sprint(nOpus+1) {
		t.Fatalf("re-append changed the manifest: count-scheduled = %s, want %d", got, nOpus+1)
	}
	readPosts()

	// --- manifest seal (approved -> scheduled) -------------------------------
	h.mustOK("manifest", "seal", "--job", jobDir)
	j = h.wantState(jobDir, model.StateScheduled)
	if s, _ := j.Extra["scheduled_at"].(string); s == "" {
		t.Fatalf("seal must record extra.scheduled_at: %v", j.Extra)
	}
	h.mustOK("manifest", "seal", "--job", jobDir) // idempotent
	var heldIDs []string
	for _, c := range clips.Clips {
		if c.ProjectID == clips.ShortsProjectID && c.Score < 70 {
			heldIDs = append(heldIDs, c.ClipID)
		}
	}
	var sealed struct {
		Clips []struct {
			ClipID         string `json:"clip_id"`
			Status         string `json:"status"`
			ScheduledPosts []struct {
				ScheduleID string `json:"schedule_id"`
				Check      *struct {
					Method   string          `json:"method"`
					Status   string          `json:"status"`
					Response json.RawMessage `json:"response"`
				} `json:"check"`
			} `json:"scheduled_posts"`
		} `json:"clips"`
	}
	readJSON(t, manifestPath, &sealed)
	held := 0
	for _, c := range sealed.Clips {
		if c.Status == "held" {
			held++
			if len(heldIDs) != 1 || c.ClipID != heldIDs[0] {
				t.Fatalf("held clip %s, want the draft's held clip %v", c.ClipID, heldIDs)
			}
		}
	}
	if held != 1 {
		t.Fatalf("seal should record the draft's one held clip in the backlog, got %d", held)
	}

	// --- watch --once: monitor (scheduled -> monitored), expire, close -------
	// The pass also runs launch-session for this job (it was never launched),
	// so the claude binary is a stub.
	before = len(h.fake.RequestsMatching("GET", "/api/publish-schedules"))
	h.mustOK("watch", "--once", "--profile", "evc", "--stable-seconds", "0")
	j = h.wantState(jobDir, model.StateMonitored)
	mon, _ := j.Extra["monitor"].(map[string]any)
	if mon["method"] != "opusclip-rest" || mon["pending"] != float64(nOpus) || mon["manifest_only"] != float64(1) || j.Extra["monitored_at"] == nil {
		t.Fatalf("monitor record: %v", j.Extra)
	}
	reads := h.fake.RequestsMatching("GET", "/api/publish-schedules")[before:]
	if len(reads) == 0 {
		t.Fatal("monitor never read the schedules back")
	}
	for _, r := range reads {
		if !strings.Contains(r.Query, "q=findByProjectAndClip") || r.Status != 200 {
			t.Fatalf("unexpected schedule read: %s?%s -> %d", r.Path, r.Query, r.Status)
		}
	}
	readJSON(t, manifestPath, &sealed)
	for _, c := range sealed.Clips {
		for _, p := range c.ScheduledPosts {
			if c.ClipID == static.ClipID { // a Buffer post: id only, nothing read back
				if p.Check == nil || p.Check.Status != "manifest_only" {
					t.Fatalf("buffer post %s: check %+v", p.ScheduleID, p.Check)
				}
				continue
			}
			var entry struct {
				ScheduleID string `json:"scheduleId"`
			}
			if p.Check == nil || p.Check.Status != "pending" || p.Check.Method != "opusclip-rest" || json.Unmarshal(p.Check.Response, &entry) != nil || entry.ScheduleID != p.ScheduleID {
				t.Fatalf("post %s: check %+v", p.ScheduleID, p.Check)
			}
		}
	}
	for _, name := range []string{"monitor", "expire", "close"} {
		b, err := os.ReadFile(filepath.Join(jobDir, "logs", name+".log"))
		if err != nil || !strings.Contains(string(b), "=== exit 0") {
			t.Fatalf("watch should have run %s with exit 0: %v\n%s", name, err, b)
		}
	}
	h.mustOK("monitor", jobDir) // idempotent

	// --- backlog: the clock is moved past backlog_days (25) ------------------
	later := time.Now().Add(26 * 24 * time.Hour).UTC().Format(time.RFC3339)
	r = h.mustOK("close", "--job", jobDir, "--now", later)
	h.wantState(jobDir, model.StateMonitored)
	if !strings.Contains(r.stderr, "1 held clips") {
		t.Fatalf("close must wait while a held clip is unexpired:\n%s", r)
	}
	r = h.mustOK("manifest", "expire", "--job", jobDir, "--now", later)
	if !strings.Contains(r.stdout, heldIDs[0]) {
		t.Fatalf("expire should expire the held clip %s:\n%s", heldIDs[0], r)
	}
	h.mustOK("close", "--job", jobDir, "--now", later)
	j = h.wantState(jobDir, model.StateClosed)
	if j.Extra["closed_at"] != later {
		t.Fatalf("closed_at = %v, want %s", j.Extra["closed_at"], later)
	}

	// --- status ------------------------------------------------------------
	r = h.mustOK("status", "--json")
	if !strings.Contains(r.stdout, j.ID) {
		t.Fatalf("status --json does not list job %s:\n%s", j.ID, r)
	}

	// --- auth hygiene --------------------------------------------------------
	if bad := h.fake.UnexpectedAuth(); len(bad) > 0 {
		t.Fatalf("the fake saw %d /api request(s) without exactly \"Bearer test\":\n%s", len(bad), dumpRequests(bad))
	}
	t.Logf("pipeline complete: job %s reached %s with %d scheduled posts; fake served %d requests", j.ID, h.job(jobDir).State, len(manifest.ScheduledPosts), len(h.fake.Requests()))
}

// TestFakeServerStandalone proves `longform fakeopus` prints a usable URL,
// which is what the setup wizard's dry run needs.
func TestFakeServerStandalone(t *testing.T) {
	h := newHarness(t)
	cmd := exec.Command(h.bin, "fakeopus", "--addr", "127.0.0.1:0", "--quiet")
	cmd.Env = h.env
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "/api") {
		time.Sleep(50 * time.Millisecond)
	}
	url := strings.TrimSpace(out.String())
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/api") {
		t.Fatalf("fakeopus printed %q, want http://127.0.0.1:<port>/api", url)
	}
	probe := exec.Command("curl", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "-H", "Authorization: Bearer test", url+"/social-accounts?q=mine")
	b, err := probe.CombinedOutput()
	if err != nil || string(b) != "200" {
		t.Fatalf("probe of standalone fake failed: %v %s", err, b)
	}
}

func dumpRequests(rs []fakeopus.Request) string {
	var sb strings.Builder
	for _, r := range rs {
		fmt.Fprintf(&sb, "#%d %s %s%s -> %d auth_matched=%v body=%s\n", r.Seq, r.Method, r.Path, q(r.Query), r.Status, r.AuthMatched, truncate(r.Body, 300))
	}
	return sb.String()
}

func q(s string) string {
	if s == "" {
		return ""
	}
	return "?" + s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func mustPretty(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
