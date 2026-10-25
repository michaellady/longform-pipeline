package cli

import (
	"encoding/json"
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

// approvedJob writes a profile (backlog_days 10) and an approved job whose
// schedule.json holds two OpusClip cells, with the client pointed at a
// fresh fake server.
func approvedJob(t *testing.T) (*model.Job, *fakeopus.Server, *opusclip.Client) {
	t.Helper()
	t.Setenv("LONGFORM_CONFIG_DIR", t.TempDir())
	t.Setenv("LONGFORM_DATA_DIR", t.TempDir())
	t.Setenv("LONGFORM_SLACK_WEBHOOK", "")
	if err := os.MkdirAll(model.ProfilesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model.ProfilePath("evc"), []byte(`{"name":"evc","inbox":"/tmp/inbox","orientation":"portrait","backlog_days":10,"notify":{"macos":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	j, err := model.NewJob("evc", strings.Repeat("c", 64), nil, time.Now().Add(-time.Hour))
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
	at := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	sched := map[string]any{"schema_version": "1", "job_id": j.ID, "cells": []map[string]any{
		{"kind": "full", "clip_id": "F", "project_id": "PF", "platform": "youtube", "account_id": "acc-yt", "title": "Full", "publish_at": at, "label": "full video"},
		{"kind": "clip", "clip_id": "A", "project_id": "PS", "platform": "youtube", "account_id": "acc-yt", "title": "A", "publish_at": at, "label": "clip 1: A"},
	}}
	b, _ := json.Marshal(sched)
	if err := os.WriteFile(j.Path("review/schedule.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	fake := fakeopus.New(fakeopus.Options{APIKey: "test"})
	api, err := fake.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fake.Close() })
	client := opusclip.New("test", opusclip.WithBaseURL(api), opusclip.WithNoRateLimit(), opusclip.WithRetry(0, time.Millisecond))
	old := newOpusClient
	newOpusClient = func() (*opusclip.Client, error) { return client, nil }
	t.Cleanup(func() { newOpusClient = old })
	return j, fake, client
}

func state(t *testing.T, j *model.Job) model.State {
	t.Helper()
	nj, err := model.LoadJob(j.Dir())
	if err != nil {
		t.Fatal(err)
	}
	return nj.State
}

func TestLifecycleCommandsEndToEnd(t *testing.T) {
	j, fake, client := approvedJob(t)

	// seal with no manifest: 65, one verdict line per cell, state unchanged.
	out, code := captureStdout(t, func() int { return runSeal([]string{"--job", j.Dir()}) })
	if code != 65 {
		t.Fatalf("seal without posts: exit %d\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"clip_id":"F"`) || !strings.Contains(lines[1], `"clip_id":"A"`) {
		t.Fatalf("verdict lines:\n%s", out)
	}
	if state(t, j) != model.StateApproved {
		t.Fatal("seal moved the state despite missing posts")
	}

	// Post both cells on the fake and append them like the skill does.
	path := manifest.PathIn(j.Dir())
	if _, err := manifest.Init(path, manifest.InitOptions{}); err != nil {
		t.Fatal(err)
	}
	m, _ := manifest.Load(path)
	sch, _ := os.ReadFile(j.Path("review/schedule.json"))
	var wrapped struct {
		Cells []json.RawMessage `json:"cells"`
	}
	_ = json.Unmarshal(sch, &wrapped)
	for _, raw := range wrapped.Cells {
		var l manifest.ScheduleLine
		_ = json.Unmarshal([]byte(`{"cell":`+string(raw)+`}`), &l)
		at, _ := time.Parse(time.RFC3339, l.Cell.PublishAt)
		r, err := client.Schedule(t.Context(), opusclip.ScheduleRequest{ProjectID: l.Cell.ProjectID, ClipID: l.Cell.ClipID, PostAccountID: l.Cell.AccountID, PublishAt: opusclip.FormatPublishAt(at), PostDetail: opusclip.PostDetail{Title: l.Cell.Title}})
		if err != nil {
			t.Fatal(err)
		}
		l.Response = r.Raw
		if err := m.AppendScheduleLine(l); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}

	if out, code := captureStdout(t, func() int { return runSeal([]string{"--job", j.Dir()}) }); code != 0 || state(t, j) != model.StateScheduled {
		t.Fatalf("seal: exit %d state %s\n%s", code, state(t, j), out)
	}
	if _, code := captureStdout(t, func() int { return runSeal([]string{"--job", j.Dir()}) }); code != 0 {
		t.Fatalf("second seal: exit %d", code)
	}

	// monitor: a vanished schedule fails with 70 and notifies once.
	ids := m.ScheduleIDs()
	fake.DropSchedule(ids[1])
	for run := 1; run <= 2; run++ {
		out, code := captureStdout(t, func() int { return runMonitor([]string{j.Dir()}) })
		if code != 70 || !strings.Contains(out, ids[1]) || state(t, j) != model.StateScheduled {
			t.Fatalf("monitor run %d: exit %d state %s\n%s", run, code, state(t, j), out)
		}
	}
	events, _ := os.ReadFile(j.Path(filepath.Join("logs", "events.log")))
	if n := strings.Count(string(events), "monitor_failed"); n != 1 {
		t.Fatalf("want one monitor_failed notification across two failing runs, got %d:\n%s", n, events)
	}

	// Re-schedule the vanished cell and append its line, as the fix hint
	// says; the new pending id replaces the old one and monitor passes.
	m, _ = manifest.Load(path)
	var again manifest.ScheduleLine
	for _, c := range m.Clips {
		for _, p := range c.ScheduledPosts {
			if p.ScheduleID == ids[1] {
				again.Cell = gates.Cell{Kind: "clip", ClipID: p.ClipID, ProjectID: p.ProjectID, Platform: p.Platform, AccountID: p.AccountID, PublishAt: p.ScheduledAtUTC, Label: p.Label}
			}
		}
	}
	at, _ := time.Parse(time.RFC3339, again.Cell.PublishAt)
	r, err := client.Schedule(t.Context(), opusclip.ScheduleRequest{ProjectID: again.Cell.ProjectID, ClipID: again.Cell.ClipID, PostAccountID: again.Cell.AccountID, PublishAt: opusclip.FormatPublishAt(at), PostDetail: opusclip.PostDetail{Title: "again"}})
	if err != nil {
		t.Fatal(err)
	}
	again.Response = r.Raw
	if err := m.AppendScheduleLine(again); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	out, code = captureStdout(t, func() int { return runMonitor([]string{"--job", j.Dir()}) })
	if code != 0 || state(t, j) != model.StateMonitored || !strings.Contains(out, `"pending": 2`) {
		t.Fatalf("monitor: exit %d state %s\n%s", code, state(t, j), out)
	}
	m, _ = manifest.Load(path)
	for _, c := range m.Clips {
		for _, p := range c.ScheduledPosts {
			if p.ScheduleID == ids[1] && (p.Check == nil || p.Check.Status != "replaced") {
				t.Fatalf("the vanished id should be marked replaced: %+v", p.Check)
			}
		}
	}

	// close: inside the 10-day window it stays; after it, it closes.
	out, code = captureStdout(t, func() int { return runClose([]string{j.Dir()}) })
	if code != 0 || state(t, j) != model.StateMonitored || !strings.Contains(out, `"closed": false`) {
		t.Fatalf("close inside window: exit %d\n%s", code, out)
	}
	later := time.Now().Add(11 * 24 * time.Hour).UTC().Format(time.RFC3339)
	out, code = captureStdout(t, func() int { return runClose([]string{"--now", later, "--job", j.Dir()}) })
	if code != 0 || state(t, j) != model.StateClosed {
		t.Fatalf("close after window: exit %d state %s\n%s", code, state(t, j), out)
	}
	if _, code := captureStdout(t, func() int { return runClose([]string{j.Dir()}) }); code != 0 {
		t.Fatalf("close on closed job: exit %d", code)
	}
}

func TestLifecycleUsageAndStateErrors(t *testing.T) {
	j, _, _ := approvedJob(t)
	for _, tc := range []struct {
		name string
		run  func() int
		want int
	}{
		{"seal no job", func() int { return runSeal(nil) }, 64},
		{"monitor two dirs", func() int { return runMonitor([]string{j.Dir(), j.Dir()}) }, 64},
		{"close bad now", func() int { return runClose([]string{"--now", "tomorrow", j.Dir()}) }, 64},
		{"monitor approved", func() int { return runMonitor([]string{j.Dir()}) }, 65},
		{"close approved", func() int { return runClose([]string{j.Dir()}) }, 65},
		{"seal missing job", func() int { return runSeal([]string{"--job", filepath.Join(t.TempDir(), "nope")}) }, 65},
	} {
		if _, code := captureStdout(t, tc.run); code != tc.want {
			t.Errorf("%s: exit %d, want %d", tc.name, code, tc.want)
		}
	}
	// An approved job without review/schedule.json is a data error, not an
	// internal one.
	if err := os.Remove(j.Path("review/schedule.json")); err != nil {
		t.Fatal(err)
	}
	if _, code := captureStdout(t, func() int { return runSeal([]string{"--job", j.Dir()}) }); code != 65 {
		t.Errorf("seal without schedule.json: exit %d, want 65", code)
	}
}
