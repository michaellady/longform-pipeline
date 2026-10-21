package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/opusclip"
)

// fakeOpus is an httptest stand-in for api.opus.pro plus the GCS upload host.
type fakeOpus struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	uploadLinks   int
	sessionStarts int
	putBytes      int64
	projectBodies []map[string]any
	failFull      bool // while true, skipCurate project creation answers 500
	listCalls     map[string]int
	clipsAfter    int // list returns clips once listCalls[project] > clipsAfter
	reRenders     map[string]json.RawMessage
	schedules     []map[string]any
	failSchedule  int // 1-based index of the cell that answers 400
}

func newFakeOpus(t *testing.T) *fakeOpus {
	f := &fakeOpus{t: t, listCalls: map[string]int{}, reRenders: map[string]json.RawMessage{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpus) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	p := r.URL.Path
	switch {
	case r.Method == "POST" && p == "/upload-links":
		f.uploadLinks++
		fmt.Fprintf(w, `{"url":"%s/gcs/signed","uploadId":"up_%d"}`, f.srv.URL, f.uploadLinks)
	case r.Method == "POST" && p == "/gcs/signed":
		f.sessionStarts++
		w.Header().Set("Location", f.srv.URL+"/gcs/session")
		w.WriteHeader(201)
	case r.Method == "PUT" && p == "/gcs/session":
		f.putBytes += int64(len(body))
		w.WriteHeader(200)
	case r.Method == "POST" && p == "/clip-projects":
		var m map[string]any
		json.Unmarshal(body, &m)
		f.projectBodies = append(f.projectBodies, m)
		cur, _ := m["curationPref"].(map[string]any)
		if cur["skipCurate"] == true {
			if f.failFull {
				http.Error(w, `{"message":"boom"}`, 500)
				return
			}
			fmt.Fprint(w, `{"data":{"projectId":"proj_full","stage":"QUEUED","sourceInfo":{"durationMs":60000}}}`)
			return
		}
		fmt.Fprint(w, `{"id":"proj_shorts","projectId":"proj_shorts","stage":"QUEUED","title":"x","sourceInfo":{"durationMs":61000}}`)
	case r.Method == "GET" && p == "/exportable-clips":
		pid := r.URL.Query().Get("projectId")
		f.listCalls[pid]++
		if f.listCalls[pid] <= f.clipsAfter {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		if pid == "proj_full" {
			fmt.Fprint(w, `{"data":[`+fullClipJSON()+`]}`)
			return
		}
		// Clip count grows on the first non-empty poll, like the live API.
		if f.listCalls[pid] == f.clipsAfter+1 {
			fmt.Fprint(w, `{"data":[`+clipJSON("proj_shorts", "S1", "Hook one", 92)+`]}`)
			return
		}
		fmt.Fprint(w, `{"data":[`+clipJSON("proj_shorts", "S1", "Hook one", 92)+`,`+clipJSON("proj_shorts", "S2", "Hook two", 80)+`]}`)
	case r.Method == "GET" && strings.HasPrefix(p, "/exportable-clips/"):
		id := strings.TrimPrefix(p, "/exportable-clips/")
		proj, clip, _ := strings.Cut(id, ".")
		c := clipJSON(proj, clip, "Title "+clip, 50)
		if proj == "proj_full" {
			c = fullClipJSON()
		}
		if r.URL.Query().Get("include") == "editingScript" {
			overlay := `{"screenOverlays":[{"fileId":"logo"}]}`
			if proj == "proj_full" {
				overlay = `{"screenOverlays":[],"screenOverlay":null}`
			}
			c = strings.TrimSuffix(c, "}") + `,"renderPref":` + overlay + `,"editingScript":{"v":1,"clip":"` + clip + `"}}`
		}
		fmt.Fprint(w, c)
	case r.Method == "POST" && strings.HasSuffix(p, "/re-render"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/exportable-clips/"), "/re-render")
		f.reRenders[id] = append(json.RawMessage(nil), body...)
		fmt.Fprint(w, `{"queued":true}`)
	case r.Method == "POST" && p == "/publish-schedules":
		var m map[string]any
		json.Unmarshal(body, &m)
		f.schedules = append(f.schedules, m)
		if len(f.schedules) == f.failSchedule {
			http.Error(w, `{"message":"bad account"}`, 400)
			return
		}
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"data":{"scheduleId":"sched_%d"}}`, len(f.schedules))
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		http.Error(w, "unexpected", 404)
	}
}

// clipJSON mirrors a curated clip from the live API: hashtags is one string,
// transcript carries __silence tokens, renderAsVideoFile is null.
func clipJSON(proj, clip, title string, score int) string {
	return fmt.Sprintf(`{"id":"%s.%s","curationId":"%s","rank":1,"score":%d,"title":"%s","description":"desc %s","hashtags":"#a #b","durationMs":45400,"isBonusClip":false,"text":"transcript __silence of %s","uriForPreview":"https://p/%s","uriForExport":"https://e/%s","judgeResult":{"hookScore":9},"renderAsVideoFile":null,"renderAsVideoPreview":null}`, proj, clip, clip, score, title, clip, clip, clip, clip)
}

// fullClipJSON mirrors the single skipCurate clip: null copy and scores.
func fullClipJSON() string {
	return `{"id":"proj_full.F1","curationId":"F1","rank":null,"score":null,"title":"slug_OpusClip Captions","description":null,"hashtags":null,"keywords":null,"durationMs":60000,"isBonusClip":false,"text":"full __silence text","uriForPreview":"https://p/F1","uriForExport":"https://e/F1","renderAsVideoFile":null,"renderAsVideoPreview":null}`
}

func setupJob(t *testing.T, f *fakeOpus, stripOverlay bool) *model.Job {
	t.Helper()
	t.Setenv("LONGFORM_CONFIG_DIR", t.TempDir())
	t.Setenv("LONGFORM_DATA_DIR", t.TempDir())
	t.Setenv("OPUSCLIP_API_KEY", "test-key")
	profile := map[string]any{
		"name": "evc", "inbox": "/tmp/inbox", "orientation": "portrait",
		"opusclip": map[string]any{
			"template_id": "tmpl_9", "strip_overlay": stripOverlay,
			"shorts":   map[string]any{"model": "ClipAnything", "prompt": "complete technique demonstrations", "durations": []int{30, 60, 90}, "aspect": "portrait", "remove_filler": true},
			"full":     map[string]any{"skip_curate": true, "aspect": "landscape"},
			"accounts": map[string]string{"youtube": "acct_yt"},
		},
	}
	b, _ := json.Marshal(profile)
	if err := os.MkdirAll(model.ProfilesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model.ProfilePath("evc"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	job, err := model.NewJob("evc", "abcdef1234567890", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []model.State{model.StateStaged, model.StatePrepared} {
		if err := job.Advance(s, "test", ""); err != nil {
			t.Fatal(err)
		}
	}
	job.Artifacts.Delivery = "delivery.mp4"
	if err := os.WriteFile(job.Path("delivery.mp4"), bytes.Repeat([]byte("mp4"), 4000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := job.Save(); err != nil {
		t.Fatal(err)
	}
	old := newOpusClient
	newOpusClient = func() (*opusclip.Client, error) {
		return opusclip.New("test-key", opusclip.WithBaseURL(f.srv.URL), opusclip.WithNoRateLimit(), opusclip.WithRetry(1, time.Millisecond)), nil
	}
	t.Cleanup(func() { newOpusClient = old })
	return job
}

func TestUploadCreatesBothProjectsAndIsIdempotent(t *testing.T) {
	f := newFakeOpus(t)
	f.failFull = true
	job := setupJob(t, f, false)

	// First run: upload succeeds, shorts project created, full project 500s
	// (retried once, still 500).
	if code := runUpload([]string{job.Dir()}); code != 75 {
		t.Fatalf("first run should fail on the full project with 75, got %d", code)
	}
	f.mu.Lock()
	f.failFull = false
	f.mu.Unlock()
	j, _ := model.LoadJob(job.Dir())
	if j.OpusClip.UploadID != "up_1" || j.OpusClip.ShortsProjectID != "proj_shorts" || j.OpusClip.FullProjectID != "" || j.State != model.StatePrepared {
		t.Fatalf("after failed run: %+v state=%s", j.OpusClip, j.State)
	}

	// Second run: nothing re-uploaded, only the missing project is created.
	if code := runUpload([]string{job.Dir()}); code != 0 {
		t.Fatalf("second run exit %d", code)
	}
	j, _ = model.LoadJob(job.Dir())
	if j.State != model.StateUploaded || j.OpusClip.FullProjectID != "proj_full" || j.OpusClip.UploadID != "up_1" {
		t.Fatalf("after second run: %+v state=%s", j.OpusClip, j.State)
	}
	if f.uploadLinks != 1 || f.sessionStarts != 1 || f.putBytes != 12000 {
		t.Fatalf("upload steps ran more than once: links=%d sessions=%d bytes=%d", f.uploadLinks, f.sessionStarts, f.putBytes)
	}
	if len(f.projectBodies) != 4 { // shorts, full(500), full(500 retry), full
		t.Fatalf("project creations = %d", len(f.projectBodies))
	}

	// Credit estimate recorded from sourceInfo.durationMs (61 s + 60 s -> 2 + 1).
	if est, _ := j.Extra[extraCreditsEstimate].(float64); est != 3 {
		t.Fatalf("credits estimate = %v, want 3 (%v)", j.Extra[extraCreditsEstimate], j.Extra[extraProjects])
	}
	if recs := projectRecords(j); len(recs) != 2 || recs[0].Kind != "shorts" || recs[0].Stage != "QUEUED" || recs[1].ID != "proj_full" || recs[1].SourceDurationMs != 60000 {
		t.Fatalf("project records = %+v", recs)
	}

	// Third run: fully idempotent, no API calls.
	before := len(f.projectBodies)
	if code := runUpload([]string{job.Dir()}); code != 0 {
		t.Fatalf("third run exit %d", code)
	}
	if len(f.projectBodies) != before || f.uploadLinks != 1 {
		t.Fatalf("idempotent run made API calls")
	}

	// Request bodies.
	shorts := f.projectBodies[0]
	if shorts["videoUrl"] != "up_1" || shorts["brandTemplateId"] != "tmpl_9" {
		t.Errorf("shorts body: %v", shorts)
	}
	if shorts["uploadedVideoAttr"].(map[string]any)["title"] != job.Slug {
		t.Errorf("shorts title = %v", shorts["uploadedVideoAttr"])
	}
	cur := shorts["curationPref"].(map[string]any)
	if cur["model"] != "ClipAnything" || cur["customPrompt"] != "complete technique demonstrations" || cur["skipCurate"] != nil {
		t.Errorf("shorts curationPref = %v", cur)
	}
	want := `[[0,30],[0,60],[0,90]]`
	if got, _ := json.Marshal(cur["clipDurations"]); string(got) != want {
		t.Errorf("clipDurations = %s want %s", got, want)
	}
	rp := shorts["renderPref"].(map[string]any)
	if rp["layoutAspectRatio"] != "portrait" || rp["quickstartConfig"].(map[string]any)["enableRemoveFillerWords"] != true {
		t.Errorf("shorts renderPref = %v", rp)
	}
	full := f.projectBodies[len(f.projectBodies)-1]
	fcur := full["curationPref"].(map[string]any)
	if fcur["skipCurate"] != true || fcur["model"] != nil || fcur["clipDurations"] != nil {
		t.Errorf("full curationPref = %v", fcur)
	}
	if full["renderPref"].(map[string]any)["layoutAspectRatio"] != "landscape" || full["videoUrl"] != "up_1" {
		t.Errorf("full body = %v", full)
	}
}

func TestWaitPollsToClipped(t *testing.T) {
	f := newFakeOpus(t)
	f.clipsAfter = 1 // first poll of each project is empty
	job := setupJob(t, f, true)
	job.OpusClip = model.OpusClipRefs{UploadID: "up_1", ShortsProjectID: "proj_shorts", FullProjectID: "proj_full"}
	if err := job.Advance(model.StateUploaded, "test", ""); err != nil {
		t.Fatal(err)
	}
	stripPace, renderPollInterval = 0, time.Millisecond

	if code := runWait([]string{"--interval", "1ms", "--ceiling", "10s", job.Dir()}); code != 0 {
		t.Fatalf("wait exit %d", code)
	}
	j, _ := model.LoadJob(job.Dir())
	if j.State != model.StateClipped || !j.OpusClip.OverlayStripped || j.OpusClip.FullClipID != "F1" {
		t.Fatalf("job after wait: state=%s %+v", j.State, j.OpusClip)
	}
	if j.Artifacts.Clips != "clips.json" || j.Artifacts.OpusTranscripts != "analysis/opus-transcripts.json" {
		t.Fatalf("artifacts = %+v", j.Artifacts)
	}
	// Polls: empty, 1 clip, 2 clips, 2 clips (stable) -> at least 4 before the
	// post-strip re-list.
	if f.listCalls["proj_shorts"] < 4 {
		t.Fatalf("expected the count to be stable across two polls, got %d polls", f.listCalls["proj_shorts"])
	}

	var cf ClipsFile
	b, err := os.ReadFile(j.Path("clips.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cf); err != nil {
		t.Fatal(err)
	}
	if cf.SchemaVersion != "1" || cf.ShortsProjectID != "proj_shorts" || cf.FullProjectID != "proj_full" || cf.FullClipID != "F1" || len(cf.Clips) != 3 || !cf.URIsExpire || cf.FetchedAt == "" {
		t.Fatalf("clips.json = %+v", cf)
	}
	c0 := cf.Clips[0]
	if c0.ProjectID != "proj_shorts" || c0.ClipID != "S1" || c0.Title != "Hook one" || c0.Score != 92 || c0.DurationSec != 45 || len(c0.Hashtags) != 2 || c0.URIExport != "https://e/S1" || c0.URIPreview != "https://p/S1" || c0.IsBonus || c0.IsFull || c0.HookScore != 9 {
		t.Fatalf("clip summary = %+v", c0)
	}
	var raw map[string]any
	if err := json.Unmarshal(c0.Raw, &raw); err != nil || raw["id"] != "proj_shorts.S1" {
		t.Fatalf("raw clip not preserved: %s", c0.Raw)
	}
	if fc := cf.Clips[2]; !fc.IsFull || fc.ClipID != "F1" || fc.Score != 0 || len(fc.Hashtags) != 0 || fc.Hashtags == nil || fc.DurationSec != 60 {
		t.Fatalf("full clip entry = %+v", cf.Clips[2])
	}

	// Generic shape check against the documented field names.
	var generic map[string]any
	json.Unmarshal(b, &generic)
	for _, k := range []string{"schema_version", "shorts_project_id", "full_project_id", "full_clip_id", "clips"} {
		if _, ok := generic[k]; !ok {
			t.Errorf("clips.json missing %s", k)
		}
	}
	first := generic["clips"].([]any)[0].(map[string]any)
	for _, k := range []string{"project_id", "clip_id", "title", "description", "hashtags", "score", "duration_sec", "uri_preview", "uri_export", "is_bonus", "raw"} {
		if _, ok := first[k]; !ok {
			t.Errorf("clip entry missing %s", k)
		}
	}

	var tf TranscriptsFile
	b, _ = os.ReadFile(j.Path("analysis/opus-transcripts.json"))
	if err := json.Unmarshal(b, &tf); err != nil || len(tf.Clips) != 3 || tf.Clips[0].Transcript != "transcript __silence of S1" || tf.Clips[0].TranscriptClean != "transcript of S1" {
		t.Fatalf("transcripts = %+v (%v)", tf, err)
	}

	// Overlay strip hit every shorts clip (overlay present) with the right
	// body and skipped the full clip (screenOverlays: []).
	for _, id := range []string{"proj_shorts.S1", "proj_shorts.S2"} {
		body, ok := f.reRenders[id]
		if !ok {
			t.Fatalf("no re-render for %s", id)
		}
		if !strings.Contains(string(body), `"disableScreenOverlay":true`) || !strings.Contains(string(body), `"editingScript":{"v":1`) {
			t.Fatalf("re-render body for %s = %s", id, body)
		}
	}
	if _, ok := f.reRenders["proj_full.F1"]; ok {
		t.Fatal("full clip without an overlay must not be re-rendered")
	}
	if n, _ := j.Extra["opusclip_overlay_strips_submitted"].(float64); n != 2 {
		t.Fatalf("strips submitted = %v", j.Extra["opusclip_overlay_strips_submitted"])
	}
	// Re-run is a no-op.
	if code := runWait([]string{job.Dir()}); code != 0 {
		t.Fatalf("re-run exit %d", code)
	}
}

func TestWaitCeilingExits75(t *testing.T) {
	f := newFakeOpus(t)
	f.clipsAfter = 1_000_000
	job := setupJob(t, f, false)
	job.OpusClip = model.OpusClipRefs{UploadID: "up_1", ShortsProjectID: "proj_shorts", FullProjectID: "proj_full"}
	if err := job.Advance(model.StateUploaded, "test", ""); err != nil {
		t.Fatal(err)
	}
	if code := runWait([]string{"--interval", "5ms", "--ceiling", "30ms", job.Dir()}); code != 75 {
		t.Fatalf("exit = %d, want 75", code)
	}
	j, _ := model.LoadJob(job.Dir())
	if j.State != model.StateUploaded {
		t.Fatalf("state changed to %s", j.State)
	}
	if _, err := os.Stat(j.Path("clips.json")); err == nil {
		t.Fatal("clips.json must not exist after a ceiling")
	}
}

func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), code
}

func TestOpusScheduleIsolatesFailures(t *testing.T) {
	f := newFakeOpus(t)
	f.failSchedule = 2
	setupJob(t, f, false)
	cells := []ScheduleCell{
		{ProjectID: "P", ClipID: "C1", Platform: "youtube", AccountID: "acct_1", Title: "one", Description: "d1", PublishAt: "2026-11-01T17:00:00-07:00"},
		{ProjectID: "P", ClipID: "C2", Platform: "youtube", AccountID: "acct_bad", Title: "two", PublishAt: "2026-11-02T10:00:00Z"},
		{ProjectID: "P", ClipID: "C3", Platform: "youtube", Title: "three", PublishAt: "2026-11-03T10:00:00Z"},           // account via --profile
		{ProjectID: "P", ClipID: "C4", Platform: "tiktok", AccountID: "acct_tt", Title: "four", PublishAt: "not-a-date"}, // local validation failure, no API call
	}
	path := filepath.Join(t.TempDir(), "cells.json")
	b, _ := json.Marshal(cells)
	os.WriteFile(path, b, 0o644)

	out, code := captureStdout(t, func() int { return runOpus([]string{"schedule", "--cells", path, "--profile", "evc"}) })
	if code != 65 {
		t.Fatalf("exit = %d, want 65 (some cells failed)", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 JSON lines, got %d:\n%s", len(lines), out)
	}
	var got []scheduleLine
	for _, l := range lines {
		var sl scheduleLine
		if err := json.Unmarshal([]byte(l), &sl); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		got = append(got, sl)
	}
	if got[0].ScheduleID != "sched_1" || got[0].Error != "" {
		t.Errorf("cell 1 = %+v", got[0])
	}
	if got[1].Status != 400 || !strings.Contains(got[1].Error, "bad account") || got[1].ScheduleID != "" {
		t.Errorf("cell 2 = %+v", got[1])
	}
	if got[2].ScheduleID != "sched_3" || got[2].Error != "" {
		t.Errorf("cell 3 (profile account) = %+v", got[2])
	}
	if !strings.Contains(got[3].Error, "not RFC3339") {
		t.Errorf("cell 4 = %+v", got[3])
	}
	if len(f.schedules) != 3 {
		t.Fatalf("API schedule calls = %d, want 3", len(f.schedules))
	}
	if f.schedules[0]["publishAt"] != "2026-11-02T00:00:00Z" {
		t.Errorf("publishAt not normalised to UTC: %v", f.schedules[0]["publishAt"])
	}
	if f.schedules[0]["postDetail"].(map[string]any)["custom"].(map[string]any)["description"] != "d1" {
		t.Errorf("postDetail = %v", f.schedules[0]["postDetail"])
	}
	if f.schedules[2]["postAccountId"] != "acct_yt" {
		t.Errorf("profile account not resolved: %v", f.schedules[2])
	}
}
