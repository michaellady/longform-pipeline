package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/fakeopus"
	"github.com/michaellady/longform-pipeline/internal/gates"
	"github.com/michaellady/longform-pipeline/internal/manifest"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/opusclip"
)

var t0 = time.Date(2026, 10, 25, 18, 0, 0, 0, time.UTC)

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

// fixture is one approved job: a full video on youtube, clip A on youtube
// and instagram, a static on instagram (Buffer), clip B held in the draft,
// clip C held by `approve except 3`, clip D dropped by `drop 4`.
type fixture struct {
	job   *model.Job
	cells []gates.Cell
}

func newFixture(t *testing.T, publishAt time.Time) *fixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LONGFORM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("LONGFORM_CONFIG_DIR", filepath.Join(root, "config"))
	j, err := model.NewJob("evc", strings.Repeat("a", 64), nil, t0.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range model.Order[1:] {
		if err := j.Advance(s, "test", ""); err != nil {
			t.Fatal(err)
		}
		if s == model.StateApproved {
			break
		}
	}
	at := publishAt.UTC().Format(time.RFC3339)
	cells := []gates.Cell{
		{Kind: "full", ClipID: "FULL1", ProjectID: "PFULL", Platform: "youtube", AccountID: "acc-yt", Title: "Full", Description: "d", PublishAt: at, Label: "full video"},
		{Kind: "clip", ClipID: "A", ProjectID: "PSHORT", Platform: "youtube", AccountID: "acc-yt", Title: "A yt", Description: "d", PublishAt: at, Label: "clip 1: A"},
		{Kind: "clip", ClipID: "A", ProjectID: "PSHORT", Platform: "instagram", AccountID: "acc-ig", SubAccountID: "ig-sub-1", Title: "A ig", PublishAt: at, Label: "clip 1: A"},
		{Kind: "static", ClipID: "s1", Platform: "instagram", AccountID: "buf-ig", Description: "quote", Label: "static s1 (quote)"},
	}
	writeJSON(t, j.Path("review/schedule.json"), map[string]any{"schema_version": "1", "job_id": j.ID, "cells": cells})
	writeJSON(t, j.Path("review/draft.json"), map[string]any{
		"schema_version": "1", "job_id": j.ID, "profile": "evc",
		"full_video": map[string]any{"project_id": "PFULL", "clip_id": "FULL1", "platforms": []string{"youtube"}},
		"clips": []map[string]any{
			{"project_id": "PSHORT", "clip_id": "A", "score": 95, "duration_sec": 30, "short_title": "A", "status": "post"},
			{"project_id": "PSHORT", "clip_id": "B", "score": 72, "duration_sec": 60, "short_title": "B", "status": "held"},
			{"project_id": "PSHORT", "clip_id": "C", "score": 71, "duration_sec": 30, "short_title": "C", "status": "post"},
			{"project_id": "PSHORT", "clip_id": "D", "score": 70, "duration_sec": 30, "short_title": "D", "status": "post"},
		},
	})
	// Rows follow OrderedClips: no publish_at on any draft cell, so score
	// descending: A=1, B=2, C=3, D=4.
	writeJSON(t, j.Path("review/decision.json"), map[string]any{"schema_version": "1", "grammar": "approve except 3; drop 4", "approved": true, "held_clips": []int{3}, "dropped_clips": []int{4}})
	writeJSON(t, j.Path("clips.json"), map[string]any{"clips": []map[string]any{{"project_id": "PSHORT", "clip_id": "A"}, {"project_id": "PFULL", "clip_id": "FULL1"}}})
	j.Artifacts.Schedule = "review/schedule.json"
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
	return &fixture{job: j, cells: cells}
}

// appendLines records schedule lines the way `manifest append-post` does.
func (f *fixture) appendLines(t *testing.T, lines ...manifest.ScheduleLine) {
	t.Helper()
	path := manifest.PathIn(f.job.Dir())
	if _, err := manifest.Init(path, manifest.InitOptions{Now: t0}); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if err := m.AppendScheduleLine(l); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
}

func opusLine(c gates.Cell, id string) manifest.ScheduleLine {
	return manifest.ScheduleLine{Cell: c, Response: json.RawMessage(`{"data":{"scheduleId":"` + id + `"}}`)}
}

func bufferLine(c gates.Cell, id string) manifest.ScheduleLine {
	return manifest.ScheduleLine{Cell: c, Response: json.RawMessage(`{"post":{"id":"` + id + `"}}`), Transport: "buffer", PostID: id}
}

func reload(t *testing.T, j *model.Job) *model.Job {
	t.Helper()
	nj, err := model.LoadJob(j.Dir())
	if err != nil {
		t.Fatal(err)
	}
	return nj
}

func TestSealMissingCellsReturnsVerdictsAndChangesNothing(t *testing.T) {
	f := newFixture(t, t0.Add(24*time.Hour))
	f.appendLines(t,
		opusLine(f.cells[1], "sched-A-yt"),
		manifest.ScheduleLine{Cell: f.cells[0], Error: "opusclip: POST /publish-schedules: HTTP 400"},
		// A Buffer line without post_id: recorded, but with no id.
		manifest.ScheduleLine{Cell: f.cells[3], Response: json.RawMessage(`{"ok":true}`), Transport: "buffer"},
	)
	res, err := Seal(f.job, SealOptions{Now: t0})
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("err = %v, want ErrMissing", err)
	}
	if res.Cells != 4 || res.Posts != 1 || len(res.Missing) != 3 {
		t.Fatalf("result = %+v", res)
	}
	byKey := map[string]Verdict{}
	for _, v := range res.Missing {
		byKey[v.ClipID+"/"+v.Platform] = v
	}
	if v := byKey["FULL1/youtube"]; !strings.Contains(v.Reason, "schedule call failed") || v.Kind != "full" {
		t.Errorf("full verdict = %+v", v)
	}
	if v := byKey["A/instagram"]; v.Reason != "no post recorded in manifest.json" || v.Field != "post" {
		t.Errorf("A/instagram verdict = %+v", v)
	}
	if v := byKey["s1/instagram"]; !strings.Contains(v.Reason, "without an id") || !strings.Contains(v.FixHint, "post_id") {
		t.Errorf("static verdict = %+v", v)
	}
	j := reload(t, f.job)
	if j.State != model.StateApproved || j.Extra[KeyScheduledAt] != nil || j.Extra[KeySeal] != nil {
		t.Fatalf("job changed: state=%s extra=%v", j.State, j.Extra)
	}
}

func TestSealCompleteAdvancesRecordsBacklogAndIsIdempotent(t *testing.T) {
	f := newFixture(t, t0.Add(24*time.Hour))
	f.appendLines(t,
		opusLine(f.cells[0], "sched-full"),
		opusLine(f.cells[1], "sched-A-yt"),
		opusLine(f.cells[2], "sched-A-ig"),
		bufferLine(f.cells[3], "buf-1"),
	)
	res, err := Seal(f.job, SealOptions{Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced || res.Posts != 4 || len(res.Missing) != 0 || strings.Join(res.Held, ",") != "B,C" {
		t.Fatalf("result = %+v", res)
	}
	j := reload(t, f.job)
	if j.State != model.StateScheduled || j.Extra[KeyScheduledAt] != "2026-10-25T18:00:00Z" {
		t.Fatalf("state=%s extra=%v", j.State, j.Extra)
	}
	last := j.History[len(j.History)-1]
	if last.From != model.StateApproved || last.To != model.StateScheduled || last.By != "manifest seal" || last.Note != "4 posts sealed" {
		t.Fatalf("transition = %+v", last)
	}
	m, err := manifest.Load(manifest.PathIn(j.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	if c := m.Find("B"); c == nil || c.Status != manifest.StatusHeld {
		t.Fatalf("B should be held: %+v", c)
	}
	if c := m.Find("C"); c == nil || c.Status != manifest.StatusHeld {
		t.Fatalf("C (approve except 3) should be held: %+v", c)
	}
	if m.Find("D") != nil {
		t.Fatal("D was dropped and must not enter the backlog")
	}
	if c := m.Find("A"); c == nil || c.Status != manifest.StatusPost {
		t.Fatalf("A posted: %+v", c)
	}

	again, err := Seal(j, SealOptions{Now: t0.Add(time.Hour)})
	if err != nil || again.Already != model.StateScheduled || again.Advanced {
		t.Fatalf("second seal: %+v %v", again, err)
	}
	if j2 := reload(t, j); len(j2.History) != len(j.History) || j2.Extra[KeyScheduledAt] != "2026-10-25T18:00:00Z" {
		t.Fatal("second seal changed the job")
	}
}

func TestSealAllowMissingRecordsThem(t *testing.T) {
	f := newFixture(t, t0.Add(24*time.Hour))
	// No manifest at all: seal creates it.
	res, err := Seal(f.job, SealOptions{AllowMissing: true, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced || res.Posts != 0 || len(res.Missing) != 4 {
		t.Fatalf("result = %+v", res)
	}
	j := reload(t, f.job)
	seal, _ := j.Extra[KeySeal].(map[string]any)
	missing, _ := seal["missing"].([]any)
	if j.State != model.StateScheduled || len(missing) != 4 {
		t.Fatalf("state=%s seal=%v", j.State, seal)
	}
	if !strings.Contains(j.History[len(j.History)-1].Note, "4 cells missing") {
		t.Fatalf("note = %q", j.History[len(j.History)-1].Note)
	}
	if _, err := os.Stat(manifest.PathIn(j.Dir())); err != nil {
		t.Fatal("seal must leave a manifest behind for monitor and close")
	}
}

func TestSealRefusesWrongState(t *testing.T) {
	f := newFixture(t, t0.Add(24*time.Hour))
	f.job.State = model.StateDrafted
	if _, err := Seal(f.job, SealOptions{}); !errors.Is(err, ErrState) {
		t.Fatalf("drafted: %v", err)
	}
	f.job.State = model.StateFailed
	if _, err := Seal(f.job, SealOptions{}); !errors.Is(err, ErrState) {
		t.Fatalf("failed: %v", err)
	}
}

// sealedAgainstFake schedules the fixture's OpusClip cells on the fake (so
// the ids are real fake ids), appends them, and seals.
func sealedAgainstFake(t *testing.T, f *fixture, fake *fakeopus.Server, client *opusclip.Client) map[string]string {
	t.Helper()
	ids := map[string]string{}
	var lines []manifest.ScheduleLine
	for _, c := range f.cells {
		if c.Kind == "static" {
			lines = append(lines, bufferLine(c, "buf-1"))
			continue
		}
		at, _ := time.Parse(time.RFC3339, c.PublishAt)
		r, err := client.Schedule(context.Background(), opusclip.ScheduleRequest{ProjectID: c.ProjectID, ClipID: c.ClipID, PostAccountID: c.AccountID, SubAccountID: c.SubAccountID, PublishAt: opusclip.FormatPublishAt(at), PostDetail: opusclip.PostDetail{Title: c.Title}})
		if err != nil {
			t.Fatal(err)
		}
		ids[c.ClipID+"/"+c.Platform] = r.ScheduleID
		l := manifest.ScheduleLine{Cell: c, Response: r.Raw}
		if c.ClipID == "FULL1" {
			l.Cell.ProjectID = "" // a legacy post: monitor must find the project in schedule.json/clips.json
		}
		lines = append(lines, l)
	}
	f.appendLines(t, lines...)
	if _, err := Seal(f.job, SealOptions{Now: t0}); err != nil {
		t.Fatal(err)
	}
	f.job = reload(t, f.job)
	return ids
}

func startFake(t *testing.T) (*fakeopus.Server, *opusclip.Client) {
	t.Helper()
	fake := fakeopus.New(fakeopus.Options{APIKey: "test"})
	api, err := fake.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fake.Close() })
	return fake, opusclip.New("test", opusclip.WithBaseURL(api), opusclip.WithNoRateLimit(), opusclip.WithRetry(0, time.Millisecond))
}

func TestMonitorReadsBackEveryScheduleAndAdvances(t *testing.T) {
	fake, client := startFake(t)
	f := newFixture(t, time.Now().Add(72*time.Hour))
	ids := sealedAgainstFake(t, f, fake, client)

	res, err := Monitor(context.Background(), f.job, MonitorOptions{Client: client})
	if err != nil {
		t.Fatalf("monitor: %v (%+v)", err, res)
	}
	if !res.Advanced || res.Method != MethodREST || res.Posts != 4 || res.Pending != 3 || res.ManifestOnly != 1 || res.NotPending != 0 || len(res.Failures) != 0 {
		t.Fatalf("result = %+v", res)
	}
	j := reload(t, f.job)
	mon, _ := j.Extra[KeyMonitor].(map[string]any)
	if j.State != model.StateMonitored || j.Extra[KeyMonitoredAt] == nil || mon["method"] != MethodREST {
		t.Fatalf("state=%s extra=%v", j.State, j.Extra)
	}
	if note := j.History[len(j.History)-1].Note; !strings.Contains(note, "3 OpusClip schedules read back pending") || !strings.Contains(note, "1 Buffer posts checked against the manifest only") {
		t.Fatalf("note = %q", note)
	}
	m, _ := manifest.Load(manifest.PathIn(j.Dir()))
	for _, c := range m.Clips {
		for _, p := range c.ScheduledPosts {
			if p.Check == nil {
				t.Fatalf("post %s has no check", p.ScheduleID)
			}
			if p.IsOpusClip() {
				var entry map[string]any
				if p.Check.Status != StatusPending || json.Unmarshal(p.Check.Response, &entry) != nil || entry["scheduleId"] != p.ScheduleID {
					t.Fatalf("opus post check = %+v (%s)", p.Check, p.Check.Response)
				}
			} else if p.Check.Status != StatusManifestOnly || p.Check.Method != MethodManifestOnly {
				t.Fatalf("buffer post check = %+v", p.Check)
			}
		}
	}
	if n := len(fake.RequestsMatching("GET", "/api/publish-schedules")); n != 2 {
		t.Fatalf("want one listing per clip (FULL1, A) = 2, got %d", n)
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %v", ids)
	}

	again, err := Monitor(context.Background(), j, MonitorOptions{Client: client})
	if err != nil || again.Already != model.StateMonitored {
		t.Fatalf("second monitor: %+v %v", again, err)
	}
}

func TestMonitorFailsOnAScheduleThatVanished(t *testing.T) {
	fake, client := startFake(t)
	f := newFixture(t, time.Now().Add(72*time.Hour))
	ids := sealedAgainstFake(t, f, fake, client)
	gone := ids["A/instagram"]
	if !fake.DropSchedule(gone) {
		t.Fatal("drop")
	}
	res, err := Monitor(context.Background(), f.job, MonitorOptions{Client: client})
	if !errors.Is(err, ErrVerify) {
		t.Fatalf("err = %v", err)
	}
	if len(res.Failures) != 1 || res.Failures[0].ScheduleID != gone || res.Failures[0].ClipID != "A" {
		t.Fatalf("failures = %+v", res.Failures)
	}
	j := reload(t, f.job)
	mon, _ := j.Extra[KeyMonitor].(map[string]any)
	if j.State != model.StateScheduled || mon["failures"] == nil || j.Extra[KeyMonitoredAt] != nil {
		t.Fatalf("state=%s extra=%v", j.State, j.Extra)
	}
	m, _ := manifest.Load(manifest.PathIn(j.Dir()))
	for _, c := range m.Clips {
		for _, p := range c.ScheduledPosts {
			if p.ScheduleID == gone && (p.Check == nil || p.Check.Status != StatusMissing) {
				t.Fatalf("vanished post check = %+v", p.Check)
			}
		}
	}
}

func TestMonitorPastPostsAreNotPendingNotVerified(t *testing.T) {
	fake, client := startFake(t)
	f := newFixture(t, time.Now().Add(72*time.Hour))
	ids := sealedAgainstFake(t, f, fake, client)
	// Once a post goes out the real API stops listing its schedule; model
	// that by dropping them and running the monitor after the publish time.
	for _, id := range ids {
		fake.DropSchedule(id)
	}
	res, err := Monitor(context.Background(), f.job, MonitorOptions{Client: client, Now: time.Now().Add(96 * time.Hour)})
	if err != nil {
		t.Fatalf("%v %+v", err, res)
	}
	if res.Pending != 0 || res.NotPending != 3 || res.ManifestOnly != 1 || !strings.Contains(res.Summary(), "not verified") {
		t.Fatalf("result = %+v summary=%q", res, res.Summary())
	}
	m, _ := manifest.Load(manifest.PathIn(f.job.Dir()))
	for _, c := range m.Clips {
		for _, p := range c.ScheduledPosts {
			if p.IsOpusClip() && (p.Check == nil || p.Check.Status != StatusNotPending || len(p.Check.Response) != 0) {
				t.Fatalf("past post check = %+v", p.Check)
			}
		}
	}
}

func TestMonitorNeedsAClientForOpusClipPosts(t *testing.T) {
	fake, client := startFake(t)
	f := newFixture(t, time.Now().Add(72*time.Hour))
	sealedAgainstFake(t, f, fake, client)
	_, err := Monitor(context.Background(), f.job, MonitorOptions{ClientErr: opusclip.ErrNoAPIKey})
	if !errors.Is(err, ErrNoClient) || !strings.Contains(err.Error(), "OPUSCLIP_API_KEY") {
		t.Fatalf("err = %v", err)
	}
	if reload(t, f.job).State != model.StateScheduled {
		t.Fatal("state moved without a check")
	}
}

func TestMonitorRefusesUnsealedJob(t *testing.T) {
	f := newFixture(t, t0)
	if _, err := Monitor(context.Background(), f.job, MonitorOptions{}); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "manifest seal") {
		t.Fatalf("err = %v", err)
	}
}

// monitoredFixture is a sealed and monitored job (manifest-only, Buffer
// post only, so no API is needed), sealed at t0.
func monitoredFixture(t *testing.T) *model.Job {
	t.Helper()
	f := newFixture(t, t0.Add(24*time.Hour))
	f.appendLines(t, bufferLine(f.cells[3], "buf-1"))
	if _, err := Seal(f.job, SealOptions{AllowMissing: true, Now: t0}); err != nil {
		t.Fatal(err)
	}
	j := reload(t, f.job)
	res, err := Monitor(context.Background(), j, MonitorOptions{Now: t0.Add(time.Hour)})
	if err != nil || res.Method != MethodManifestOnly {
		t.Fatalf("monitor: %+v %v", res, err)
	}
	return reload(t, j)
}

func TestCloseWaitsForTheBacklogWindowAndHeldClips(t *testing.T) {
	j := monitoredFixture(t)
	days := 25
	ends := t0.Add(25 * 24 * time.Hour)

	// Inside the window: nothing changes.
	res, err := Close(j, CloseOptions{BacklogDays: days, Now: ends.Add(-time.Minute)})
	if err != nil || res.Closed || res.Held != 2 || res.BacklogEnds != "2026-11-19T18:00:00Z" {
		t.Fatalf("inside window: %+v %v", res, err)
	}
	// Window over but held clips not yet expired: still waits.
	res, err = Close(j, CloseOptions{BacklogDays: days, Now: ends.Add(time.Minute)})
	if err != nil || res.Closed || res.Held != 2 {
		t.Fatalf("held clips left: %+v %v", res, err)
	}
	if reload(t, j).State != model.StateMonitored {
		t.Fatal("close advanced too early")
	}

	// manifest expire marks the held clips expired (they date from the job's
	// creation, 2 days before t0); then close advances.
	path := manifest.PathIn(j.Dir())
	m, _ := manifest.Load(path)
	if got := m.Expire(days, j.CreatedAt, ends.Add(time.Minute)); len(got) != 2 {
		t.Fatalf("expired %d", len(got))
	}
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	res, err = Close(j, CloseOptions{BacklogDays: days, Now: ends.Add(time.Minute)})
	if err != nil || !res.Closed || res.Expired != 2 || res.Held != 0 {
		t.Fatalf("after expiry: %+v %v", res, err)
	}
	c := reload(t, j)
	if c.State != model.StateClosed || c.Extra[KeyClosedAt] != ends.Add(time.Minute).UTC().Format(time.RFC3339) {
		t.Fatalf("state=%s extra=%v", c.State, c.Extra)
	}
	if note := c.History[len(c.History)-1].Note; note != "backlog ended 2026-11-19; 2 held clips expired" {
		t.Fatalf("note = %q", note)
	}
	again, err := Close(c, CloseOptions{BacklogDays: days, Now: ends.Add(time.Hour)})
	if err != nil || again.Already != model.StateClosed {
		t.Fatalf("second close: %+v %v", again, err)
	}
}

func TestCloseRefusesBeforeMonitored(t *testing.T) {
	f := newFixture(t, t0)
	if _, err := Close(f.job, CloseOptions{BacklogDays: 25}); !errors.Is(err, ErrState) {
		t.Fatalf("err = %v", err)
	}
}

// TestMonitorFlagsOverduePending: a schedule still listed as pending long
// after its publish time (the probe found such stale schedules) is pending,
// not a failure, but is counted and described as overdue.
func TestMonitorFlagsOverduePending(t *testing.T) {
	fake, client := startFake(t)
	f := newFixture(t, time.Now().Add(72*time.Hour))
	sealedAgainstFake(t, f, fake, client)
	res, err := Monitor(context.Background(), f.job, MonitorOptions{Client: client, Now: time.Now().Add(96 * time.Hour)})
	if err != nil {
		t.Fatalf("%v %+v", err, res)
	}
	if res.Pending != 3 || res.Overdue != 3 || !strings.Contains(res.Summary(), "overdue") {
		t.Fatalf("result = %+v summary=%q", res, res.Summary())
	}
	m, _ := manifest.Load(manifest.PathIn(f.job.Dir()))
	for _, c := range m.Clips {
		for _, p := range c.ScheduledPosts {
			if p.IsOpusClip() && (p.Check == nil || p.Check.Status != StatusPending || !strings.Contains(p.Check.Note, "has not gone out")) {
				t.Fatalf("overdue check = %+v", p.Check)
			}
		}
	}
}
