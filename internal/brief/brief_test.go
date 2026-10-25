package brief

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

const fixtureDraft = `{
  "schema_version": "1", "job_id": "JOB", "profile": "evc",
  "full_video": {"project_id": "pf", "clip_id": "cf", "title": "Full title", "description": "Full description",
    "chapters": [{"at_ms": 0, "title": "Intro"}, {"at_ms": 95000, "title": "The point"}],
    "thumbnails": [{"path": "review/thumb-1.jpg", "chosen": true}, {"path": "review/thumb-2.jpg", "chosen": false}],
    "platforms": ["youtube", "instagram", "linkedin_page", "linkedin_profile", "tiktok"]},
  "clips": [
    {"project_id": "ps", "clip_id": "c-late", "score": 90, "duration_sec": 45, "short_title": "Late but great", "status": "post", "preview_url": "https://x/late",
     "cells": [{"platform": "youtube", "account_id": "yt", "sub_account_id": "", "title": "Late", "description": "d", "hashtags": ["#a"], "publish_at": "2026-10-24T16:00:00Z"}]},
    {"project_id": "ps", "clip_id": "c-early-lo", "score": 75, "duration_sec": 58, "short_title": "Early low", "status": "post", "preview_url": "https://x/early-lo",
     "cells": [{"platform": "instagram", "account_id": "ig", "sub_account_id": "", "title": "", "description": "d", "hashtags": [], "publish_at": "2026-10-23T16:00:00Z"},
               {"platform": "linkedin_page", "account_id": "li", "sub_account_id": "", "title": "", "description": "d", "hashtags": [], "publish_at": "2026-10-23T19:00:00Z"}]},
    {"project_id": "ps", "clip_id": "c-early-hi", "score": 82, "duration_sec": 30, "short_title": "Early high", "status": "post", "preview_url": "https://x/early-hi",
     "cells": [{"platform": "youtube", "account_id": "yt", "sub_account_id": "", "title": "EH", "description": "d", "hashtags": ["#b"], "publish_at": "2026-10-23T16:00:00Z"},
               {"platform": "tiktok", "account_id": "tt", "sub_account_id": "", "title": "", "description": "d", "hashtags": [], "publish_at": "2026-10-23T16:00:00Z"}]},
    {"project_id": "ps", "clip_id": "c-held", "score": 71, "duration_sec": 40, "short_title": "Held one", "status": "held", "preview_url": "", "cells": []}
  ],
  "statics": [
    {"id": "s1", "format": "quote", "platforms": ["instagram", "linkedin_page"], "caption": "A quote", "inputs": {"quote": "Ship it", "attribution": "Mike"}},
    {"id": "s2", "format": "carousel", "platforms": ["instagram"], "caption": "Slides", "inputs": {"slides": [{"kind": "hook", "text": "Hi"}, {"kind": "cta", "text": "Bye"}]}}
  ],
  "censored": [{"word": "damn", "start_ms": 61000, "end_ms": 61400}],
  "stabilization": {"before": "review/stabilize-before.jpg", "after": "review/stabilize-after.jpg"},
  "credits": {"spent": 32, "to_spend": 6},
  "decisions": [{"id": "d1", "question": "Which hook?", "options": ["Hook A", "Hook B"], "default": "Hook A"}]
}`

func fixtureJob(t *testing.T) (*model.Job, *Draft) {
	t.Helper()
	t.Setenv("LONGFORM_DATA_DIR", t.TempDir())
	t.Setenv("LONGFORM_CONFIG_DIR", t.TempDir())
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	job, err := model.NewJob("evc", "abcdef0123456789", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	job.Delivery = &model.Probe{DurationMs: 15*60*1000 + 7000}
	for _, s := range []model.State{model.StateStaged, model.StatePrepared, model.StateUploaded, model.StateClipped} {
		if err := job.Advance(s, "test", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(job.Path(DraftFile), []byte(strings.ReplaceAll(fixtureDraft, "JOB", job.ID)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := LoadDraft(job.Dir())
	if err != nil {
		t.Fatal(err)
	}
	return job, d
}

func testProfile() *model.Profile {
	return &model.Profile{Name: "evc", Timezone: "America/Los_Angeles", SlotsLocal: []string{"09:00"},
		Brand:    model.Brand{Display: "Brand", Site: "https://example.com"},
		OpusClip: model.OpusClipConfig{Accounts: map[string]string{"youtube": "yt-acct", "linkedin_page": "li-page"}, SubAccounts: map[string]string{"linkedin_page": "li-sub"}},
		Buffer:   model.BufferConfig{Channels: map[string]string{"instagram": "buf-ig", "linkedin": "buf-li"}}}
}

func TestRenderSectionOrderAndTable(t *testing.T) {
	job, d := fixtureJob(t)
	md, err := WriteBrief(job, testProfile(), d, time.Date(2026, 10, 21, 17, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if job.State != model.StateDrafted {
		t.Fatalf("state = %s, want drafted", job.State)
	}
	if job.Artifacts.Brief != BriefFile {
		t.Fatalf("Artifacts.Brief = %q", job.Artifacts.Brief)
	}
	// Header before the first section.
	head := md[:strings.Index(md, "## Full video")]
	for _, want := range []string{"# Brief: " + job.ID, "Profile: `evc`", "Source duration: 15:07", "Credits: 32 spent, 6 to spend"} {
		if !strings.Contains(head, want) {
			t.Errorf("header missing %q\n%s", want, head)
		}
	}
	last := -1
	for _, h := range SectionOrder {
		i := strings.Index(md, "\n"+h+"\n")
		if i < 0 {
			t.Fatalf("section %q missing", h)
		}
		if i < last {
			t.Fatalf("section %q out of order", h)
		}
		last = i
	}
	if !strings.HasSuffix(strings.TrimSpace(md), strings.TrimSpace(GrammarHelp)) {
		t.Errorf("grammar help line is not last")
	}
	// Clip table rows: sorted by publish_at (early first), then score desc, held (unslotted) last.
	var rows []string
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "| ") && !strings.HasPrefix(line, "| #") && strings.Contains(line, "preview") || strings.Contains(line, "Held one") {
			rows = append(rows, line)
		}
	}
	wantOrder := []string{"Early high", "Early low", "Late but great", "Held one"}
	if len(rows) != len(wantOrder) {
		t.Fatalf("got %d rows:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	for i, w := range wantOrder {
		if !strings.HasPrefix(rows[i], "| "+itoa(i+1)+" |") || !strings.Contains(rows[i], w) {
			t.Errorf("row %d = %q, want %q", i+1, rows[i], w)
		}
	}
	if !strings.Contains(rows[0], "youtube, tiktok") || !strings.Contains(rows[0], "Fri 23 Oct 09:00") {
		t.Errorf("row 1 platforms/slot wrong: %s", rows[0])
	}
	if !strings.Contains(rows[1], "Fri 23 Oct 09:00, Fri 23 Oct 12:00") {
		t.Errorf("row 2 should list both local slots: %s", rows[1])
	}
	if !strings.Contains(rows[3], "| hold |") {
		t.Errorf("held clip should show hold: %s", rows[3])
	}
	for _, want := range []string{"- 01:35 The point", "(chosen)", "`damn` at 01:01–01:01", "![before](review/stabilize-before.jpg)", "**d1** — Which hook?", "Hook A (default)", "_Not rendered yet._"} {
		if !strings.Contains(md, want) {
			t.Errorf("brief missing %q", want)
		}
	}
	// Idempotent re-render on a drafted job.
	if _, err := WriteBrief(job, testProfile(), d, time.Now()); err != nil || job.State != model.StateDrafted {
		t.Fatalf("re-render: err=%v state=%s", err, job.State)
	}
}

func TestRenderShowsRenderedStatics(t *testing.T) {
	job, d := fixtureJob(t)
	ix := StaticsIndex{ID: "s1", Format: "quote", Files: []string{"quote.png"}, Platforms: map[string][]string{"instagram": {"quote.png"}}}
	b, _ := json.Marshal(ix)
	os.MkdirAll(job.Path("statics/s1"), 0o755)
	os.WriteFile(job.Path("statics/s1/index.json"), b, 0o644)
	md := Render(job, nil, d, time.Now())
	if !strings.Contains(md, "![quote](statics/s1/quote.png)") {
		t.Errorf("rendered static image link missing:\n%s", md)
	}
}

func TestParseGrammar(t *testing.T) {
	_, d := fixtureJob(t)
	cases := []struct {
		grammar string
		check   func(r *DecisionRecord) string
	}{
		{"approve", func(r *DecisionRecord) string {
			if !r.Approved || len(r.HeldClips) != 0 || r.Thumbnail != 1 || r.Decisions["d1"] != "Hook A" {
				return "plain approve"
			}
			return ""
		}},
		{"approve except 2,3", func(r *DecisionRecord) string {
			if !r.Approved || len(r.HeldClips) != 2 || r.HeldClips[0] != 2 || r.HeldClips[1] != 3 {
				return "held rows"
			}
			return ""
		}},
		{"approve except 2, 3; hold linkedin", func(r *DecisionRecord) string {
			if len(r.HeldClips) != 2 || len(r.HeldPlatforms) != 1 || r.HeldPlatforms[0] != "linkedin" {
				return "comma-space numbers then hold"
			}
			return ""
		}},
		{"approve except 2, hold tiktok", func(r *DecisionRecord) string {
			if len(r.HeldClips) != 1 || r.HeldClips[0] != 2 || len(r.HeldPlatforms) != 1 || r.HeldPlatforms[0] != "tiktok" {
				return "comma separates commands when not followed by a number"
			}
			return ""
		}},
		{"reject 4-second pipeline test, not for publishing; rerecord", func(r *DecisionRecord) string {
			if r.Approved || r.Note != "4-second pipeline test, not for publishing; rerecord" {
				return "reject keeps the note verbatim, punctuation included"
			}
			return ""
		}},
		{"hold linkedin; reject: too short", func(r *DecisionRecord) string {
			if r.Approved || len(r.HeldPlatforms) != 1 || r.Note != "too short" {
				return "reject after another clause, colon stripped"
			}
			return ""
		}},
		{"reject", func(r *DecisionRecord) string {
			if r.Approved || r.Note != "" {
				return "bare reject has an empty note"
			}
			return ""
		}},
		{"hold instagram; drop 1", func(r *DecisionRecord) string {
			if !r.Approved || len(r.DroppedClips) != 1 || r.DroppedClips[0] != 1 || r.HeldPlatforms[0] != "instagram" {
				return "hold+drop"
			}
			return ""
		}},
		{"swap thumbnail 2", func(r *DecisionRecord) string {
			if r.Thumbnail != 2 {
				return "thumbnail"
			}
			return ""
		}},
		{"decide d1 Hook B", func(r *DecisionRecord) string {
			if r.Decisions["d1"] != "Hook B" {
				return "decide by text"
			}
			return ""
		}},
		{"decide d1 2; approve", func(r *DecisionRecord) string {
			if r.Decisions["d1"] != "Hook B" || !r.Approved {
				return "decide by number"
			}
			return ""
		}},
		{"reject the hooks are flat", func(r *DecisionRecord) string {
			if r.Approved || r.Note != "the hooks are flat" {
				return "reject with note"
			}
			return ""
		}},
		{"APPROVE EXCEPT 4; Hold LinkedIn", func(r *DecisionRecord) string {
			if len(r.HeldClips) != 1 || r.HeldClips[0] != 4 || r.HeldPlatforms[0] != "linkedin" {
				return "case-insensitive"
			}
			return ""
		}},
	}
	for _, c := range cases {
		r, err := ParseGrammar(c.grammar, d)
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.grammar, err)
			continue
		}
		if msg := c.check(r); msg != "" {
			t.Errorf("%q: %s: %+v", c.grammar, msg, r)
		}
	}
	bad := map[string]string{
		"":                       "empty",
		"approve; frobnicate":    `unknown token "frobnicate"`,
		"aprove":                 `unknown token "aprove"`,
		"approve except 9":       "out of range",
		"approve except x":       `unknown token "x"`,
		"hold myspace":           `unknown platform "myspace"`,
		"swap 2":                 "expected thumbnail",
		"swap thumbnail 5":       "out of range",
		"drop":                   "at least one clip row",
		"decide d9 x":            `unknown decision id "d9"`,
		"decide d1 Hook C":       "is not one of",
		"approve except 2 extra": `unknown token "extra"`,
	}
	for g, want := range bad {
		_, err := ParseGrammar(g, d)
		if err == nil {
			t.Errorf("%q: expected error", g)
			continue
		}
		if !errors.Is(err, ErrGrammar) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %q does not mention %q", g, err, want)
		}
	}
}

func TestApplyDecisionAndSchedule(t *testing.T) {
	job, d := fixtureJob(t)
	prof := testProfile()
	if _, err := WriteBrief(job, prof, d, time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 17, 0, 0, 0, time.UTC) // 10:00 PDT -> next 09:00 PDT is Oct 22 16:00Z
	rec, err := ParseGrammar("approve except 3; hold linkedin; swap thumbnail 2", d)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := ApplyDecision(job, prof, d, rec, now)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != model.StateApproved || job.Artifacts.Decision != DecisionFile || job.Artifacts.Schedule != ScheduleFile {
		t.Fatalf("job after approve: state=%s artifacts=%+v", job.State, job.Artifacts)
	}
	for _, f := range []string{DecisionFile, ScheduleFile} {
		if _, err := os.Stat(job.Path(f)); err != nil {
			t.Fatal(err)
		}
	}
	var onDisk DecisionRecord
	b, _ := os.ReadFile(job.Path(DecisionFile))
	if err := json.Unmarshal(b, &onDisk); err != nil || onDisk.Thumbnail != 2 || onDisk.SchemaVersion != "1" || onDisk.Grammar == "" || onDisk.At.IsZero() {
		t.Fatalf("decision.json: %v %+v", err, onDisk)
	}
	kinds := map[string]int{}
	for _, c := range sch.Cells {
		kinds[c.Kind]++
		if strings.HasPrefix(c.Platform, "linkedin") {
			t.Errorf("linkedin cell survived hold: %+v", c)
		}
		if c.Hashtags == nil {
			t.Errorf("hashtags must be [] not null: %+v", c)
		}
	}
	// full: youtube, instagram, tiktok (linkedin_* held) = 3
	// clips: row1 Early high (youtube, tiktok) + row2 Early low (instagram; linkedin held) = 3; row3 held; row4 held in draft
	// statics: s1 instagram (linkedin held) + s2 instagram = 2
	if kinds["full"] != 3 || kinds["clip"] != 3 || kinds["static"] != 2 {
		t.Fatalf("cell kinds = %v\n%+v", kinds, sch.Cells)
	}
	for _, c := range sch.Cells {
		switch {
		case c.Kind == "full" && c.Platform == "youtube":
			if c.AccountID != "yt-acct" || c.PublishAt != "2026-10-22T16:00:00Z" || c.DurationSec != 907 || c.ClipID != "cf" {
				t.Errorf("full youtube cell: %+v", c)
			}
		case c.Kind == "clip" && c.ClipID == "c-late":
			t.Errorf("row 3 should be held: %+v", c)
		case c.Kind == "static" && c.ClipID == "s1":
			if c.AccountID != "buf-ig" || c.Description != "A quote" || c.PublishAt != "" {
				t.Errorf("static cell: %+v", c)
			}
		}
	}
	// Re-applying on an approved job is idempotent.
	if _, err := ApplyDecision(job, prof, d, rec, now); err != nil || job.State != model.StateApproved {
		t.Fatalf("re-apply: %v %s", err, job.State)
	}
	// Reject from approved goes back to drafted with a note.
	rej, _ := ParseGrammar("reject needs new thumbnails", d)
	sch, err = ApplyDecision(job, prof, d, rej, now)
	if err != nil || job.State != model.StateDrafted || len(sch.Cells) != 0 {
		t.Fatalf("reject: err=%v state=%s cells=%d", err, job.State, len(sch.Cells))
	}
	last := job.History[len(job.History)-1]
	if last.Note != "rejected: needs new thumbnails" {
		t.Errorf("history note = %q", last.Note)
	}
}

func TestApplyDecisionNeedsDrafted(t *testing.T) {
	job, d := fixtureJob(t)
	rec, _ := ParseGrammar("approve", d)
	if _, err := ApplyDecision(job, nil, d, rec, time.Now()); err == nil {
		t.Fatal("expected error on a clipped job")
	}
}

func TestLoadDraftRejectsBadStatus(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "review"), 0o755)
	os.WriteFile(filepath.Join(dir, DraftFile), []byte(`{"schema_version":"1","clips":[{"clip_id":"x","status":"maybe"}]}`), 0o644)
	if _, err := LoadDraft(dir); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("err = %v", err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
