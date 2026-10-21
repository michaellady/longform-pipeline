package slots

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/gates"
	"github.com/michaellady/longform-pipeline/internal/model"
)

func testProfile() *model.Profile {
	return &model.Profile{
		Name: "t", Inbox: "/tmp", Orientation: "portrait", Timezone: "America/Los_Angeles",
		SlotsLocal: []string{"09:00", "12:00", "15:00", "18:00", "21:00"}, BacklogDays: 25,
		Cadence: map[string]int{"youtube": 2, "instagram": 1, "linkedin": 1, "tiktok": 2, "facebook": 1},
		Censor:  model.CensorConfig{Mode: "beep"},
		// Probe: no X account connected.
		OpusClip: model.OpusClipConfig{Shorts: model.CurationConfig{Model: "ClipBasic"}, Accounts: map[string]string{
			"youtube": "yt-acc", "instagram": "ig-acc", "facebook": "fb-acc", "linkedin_page": "li-acc", "tiktok": "tt-acc", "threads": "th-acc",
		}},
		Buffer: model.BufferConfig{Channels: map[string]string{"instagram": "buf-ig"}},
	}
}

// Probe (1): a platform with no connected account is skipped, never placed.
// Probe (2): cadence is per (platform, sub_account_id), so two Facebook
// pages share a day while one page rolls over.
func TestPropose_SkipUnconnectedAndPerSubAccountCadence(t *testing.T) {
	p := testProfile()
	approved := time.Date(2026, 10, 21, 17, 30, 0, 0, time.UTC)
	cells := []gates.Cell{
		{Kind: gates.KindClip, ClipID: "x1", Platform: "x"},
		{Kind: gates.KindClip, ClipID: "fb-a1", Platform: "facebook", SubAccountID: "pageA"},
		{Kind: gates.KindClip, ClipID: "fb-b1", Platform: "facebook", SubAccountID: "pageB"},
		{Kind: gates.KindClip, ClipID: "fb-a2", Platform: "facebook", SubAccountID: "pageA"},
	}
	out, sum, err := Propose(p, cells, nil, Options{ApprovedAt: approved, Now: approved})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Status != gates.StatusSkipped || out[0].PublishAt != "" || sum.Skipped != 1 {
		t.Errorf("x must be skipped: %+v %+v", out[0], sum)
	}
	if at(t, out[1]) != "2026-10-23T16:00:00Z" || at(t, out[2]) != "2026-10-23T16:00:00Z" {
		t.Errorf("two pages share day one: %s %s", at(t, out[1]), at(t, out[2]))
	}
	if at(t, out[3]) != "2026-10-24T16:00:00Z" {
		t.Errorf("second post on pageA rolls to day two, got %s", at(t, out[3]))
	}
}

func clips(platform string, n int) []gates.Cell {
	out := make([]gates.Cell, n)
	for i := range out {
		out[i] = gates.Cell{Kind: gates.KindClip, ClipID: platform[:2] + string(rune('a'+i)), Platform: platform, Title: "t"}
	}
	return out
}

func at(t *testing.T, c gates.Cell) string {
	t.Helper()
	if c.PublishAt == "" {
		return "held"
	}
	return c.PublishAt
}

// Golden: approval at 2026-10-21 10:30 PDT (17:30Z). Full video goes to the
// next 09:00 local that is >= 2h away: 2026-10-22 09:00 PDT = 16:00Z. Clips
// start 10-23, youtube 2/day at 09:00 and 12:00, then roll.
func TestPropose_FullNextMorningClipsRollOver(t *testing.T) {
	p := testProfile()
	approved := time.Date(2026, 10, 21, 17, 30, 0, 0, time.UTC)
	cells := append([]gates.Cell{{Kind: gates.KindFull, ClipID: "full", Platform: "youtube"}}, clips("youtube", 5)...)
	out, sum, err := Propose(p, cells, nil, Options{ApprovedAt: approved, Now: approved})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"2026-10-22T16:00:00Z", // full, 09:00 PDT
		"2026-10-23T16:00:00Z", // 09:00
		"2026-10-23T19:00:00Z", // 12:00
		"2026-10-24T16:00:00Z",
		"2026-10-24T19:00:00Z",
		"2026-10-25T16:00:00Z",
	}
	for i, w := range want {
		if got := at(t, out[i]); got != w {
			t.Errorf("cell %d (%s): got %s want %s", i, out[i].ClipID, got, w)
		}
	}
	if sum.Full != 1 || sum.Clips != 5 || sum.Held != 0 || sum.FullDay != "2026-10-22" {
		t.Errorf("summary %+v", sum)
	}
}

// The 2h lead rule: approving at 08:00 local skips today's 09:00.
func TestPropose_TwoHourLead(t *testing.T) {
	p := testProfile()
	loc, _ := time.LoadLocation(p.Timezone)
	early := time.Date(2026, 10, 21, 6, 59, 0, 0, loc) // 09:00 is 2h01 away: ok
	out, _, _ := Propose(p, []gates.Cell{{Kind: gates.KindFull, ClipID: "f", Platform: "youtube"}}, nil, Options{ApprovedAt: early, Now: early})
	if at(t, out[0]) != "2026-10-21T16:00:00Z" {
		t.Errorf("06:59 approval should take today 09:00, got %s", at(t, out[0]))
	}
	late := time.Date(2026, 10, 21, 7, 1, 0, 0, loc)
	out, _, _ = Propose(p, []gates.Cell{{Kind: gates.KindFull, ClipID: "f", Platform: "youtube"}}, nil, Options{ApprovedAt: late, Now: late})
	if at(t, out[0]) != "2026-10-22T16:00:00Z" {
		t.Errorf("07:01 approval should roll to tomorrow, got %s", at(t, out[0]))
	}
	// --from pushes the anchor later, never earlier.
	out, _, _ = Propose(p, []gates.Cell{{Kind: gates.KindFull, ClipID: "f", Platform: "youtube"}}, nil, Options{ApprovedAt: late, Now: late, From: "2026-11-01"})
	if at(t, out[0]) != "2026-11-01T17:00:00Z" { // 09:00 PST: DST ended that morning
		t.Errorf("--from should move the full video, got %s", at(t, out[0]))
	}
}

// Existing queue counting: one live instagram post on the first clip day
// means the new instagram clip goes to the day after; a live youtube post at
// 09:00 pushes the new one to 12:00 the same day.
func TestPropose_ExistingQueueCounts(t *testing.T) {
	p := testProfile()
	approved := time.Date(2026, 10, 21, 17, 30, 0, 0, time.UTC)
	existing := []gates.Cell{
		{Kind: gates.KindClip, ClipID: "live-ig", Platform: "instagram", PublishAt: "2026-10-23T16:00:00Z"},
		{Kind: gates.KindClip, ClipID: "live-yt", Platform: "youtube", PublishAt: "2026-10-23T16:00:00Z"},
	}
	cells := []gates.Cell{
		{Kind: gates.KindClip, ClipID: "ig1", Platform: "instagram"},
		{Kind: gates.KindClip, ClipID: "yt1", Platform: "youtube"},
		{Kind: gates.KindClip, ClipID: "yt2", Platform: "youtube"},
	}
	out, _, err := Propose(p, cells, existing, Options{ApprovedAt: approved, Now: approved})
	if err != nil {
		t.Fatal(err)
	}
	if got := at(t, out[0]); got != "2026-10-24T16:00:00Z" {
		t.Errorf("ig1 should skip the day the live post fills, got %s", got)
	}
	if got := at(t, out[1]); got != "2026-10-23T19:00:00Z" {
		t.Errorf("yt1 should take 12:00 beside the live 09:00, got %s", got)
	}
	if got := at(t, out[2]); got != "2026-10-24T16:00:00Z" {
		t.Errorf("yt2 rolls to the next day, got %s", got)
	}
}

// Backlog horizon: instagram 1/day with BacklogDays 3 places three clips and
// holds the fourth with no publish_at. Cells already scheduled are kept.
func TestPropose_HeldPastBacklogAndKept(t *testing.T) {
	p := testProfile()
	p.BacklogDays = 3
	approved := time.Date(2026, 10, 21, 17, 30, 0, 0, time.UTC)
	cells := append(clips("instagram", 4), gates.Cell{Kind: gates.KindClip, ClipID: "pre", Platform: "tiktok", PublishAt: "2026-12-01T00:00:00Z"})
	out, sum, err := Propose(p, cells, nil, Options{ApprovedAt: approved, Now: approved})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if out[i].Held() || out[i].PublishAt == "" {
			t.Errorf("clip %d should be placed, got %+v", i, out[i])
		}
	}
	if !out[3].Held() || out[3].PublishAt != "" {
		t.Errorf("fourth clip must be held with empty publish_at, got %+v", out[3])
	}
	if out[4].PublishAt != "2026-12-01T00:00:00Z" {
		t.Errorf("pre-scheduled cell must be kept, got %+v", out[4])
	}
	if sum.Held != 1 || sum.Kept != 1 || sum.Clips != 3 {
		t.Errorf("summary %+v", sum)
	}
	// A platform with no cadence is held outright.
	out, _, _ = Propose(p, clips("threads", 1), nil, Options{ApprovedAt: approved, Now: approved})
	if !out[0].Held() {
		t.Errorf("no-cadence platform must be held, got %+v", out[0])
	}
}

// Statics: from day two, one per platform per day, in a slot the platform's
// clip does not already use.
func TestPropose_StaticsOnePerPlatformPerDay(t *testing.T) {
	p := testProfile()
	approved := time.Date(2026, 10, 21, 17, 30, 0, 0, time.UTC)
	cells := []gates.Cell{
		{Kind: gates.KindClip, ClipID: "c1", Platform: "instagram"},
		{Kind: gates.KindStatic, ClipID: "s1", Platform: "instagram"},
		{Kind: gates.KindStatic, ClipID: "s2", Platform: "instagram"},
	}
	out, sum, _ := Propose(p, cells, nil, Options{ApprovedAt: approved, Now: approved})
	if at(t, out[0]) != "2026-10-23T16:00:00Z" || at(t, out[1]) != "2026-10-23T19:00:00Z" || at(t, out[2]) != "2026-10-24T16:00:00Z" {
		t.Errorf("statics placement wrong: %s %s %s", at(t, out[0]), at(t, out[1]), at(t, out[2]))
	}
	if sum.Statics != 2 {
		t.Errorf("summary %+v", sum)
	}
}

// DST: America/Los_Angeles falls back on 2026-11-01. The wall-clock 09:00
// slot is kept, so the UTC offset shifts from 16:00Z (PDT, 10-31) to 17:00Z
// (PST, 11-01 onward) and every day still counts as its own local day.
func TestPropose_DSTBoundaryKeepsLocalSlot(t *testing.T) {
	p := testProfile()
	p.Cadence["youtube"] = 1
	approved := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC) // 13:00 PDT; full 10-30, clips from 10-31
	out, _, err := Propose(p, clips("youtube", 3), nil, Options{ApprovedAt: approved, Now: approved})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-10-31T16:00:00Z", "2026-11-01T17:00:00Z", "2026-11-02T17:00:00Z"}
	for i, w := range want {
		if got := at(t, out[i]); got != w {
			t.Errorf("DST day %d: got %s want %s", i, got, w)
		}
	}
	// Existing cells are bucketed by LOCAL day: 2026-11-02T07:59Z is still
	// 2026-11-01 23:59 PST, so it fills 11-01, not 11-02.
	existing := []gates.Cell{
		{Kind: gates.KindClip, ClipID: "e0", Platform: "youtube", PublishAt: "2026-10-31T16:00:00Z"},
		{Kind: gates.KindClip, ClipID: "e1", Platform: "youtube", PublishAt: "2026-11-02T07:59:00Z"},
	}
	out, _, _ = Propose(p, clips("youtube", 1), existing, Options{ApprovedAt: approved, Now: approved})
	if got := at(t, out[0]); got != "2026-11-02T17:00:00Z" {
		t.Errorf("existing late-night post must consume 11-01, got %s", got)
	}
}

func TestMain_RoundTrip(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LONGFORM_CONFIG_DIR", cfg)
	os.MkdirAll(filepath.Join(cfg, "profiles"), 0o755)
	pb, _ := json.Marshal(testProfile())
	os.WriteFile(filepath.Join(cfg, "profiles", "t.json"), pb, 0o644)
	dir := t.TempDir()
	cellsPath := filepath.Join(dir, "cells.json")
	var buf bytes.Buffer
	gates.WriteCells(&buf, clips("youtube", 1))
	os.WriteFile(cellsPath, buf.Bytes(), 0o644)
	var stdout, stderr bytes.Buffer
	code := Main([]string{"propose", "--profile", "t", "--cells", cellsPath, "--approved-at", "2026-10-21T17:30:00Z"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out, err := gates.ReadCells(&stdout)
	if err != nil || len(out) != 1 || out[0].PublishAt != "2026-10-23T16:00:00Z" {
		t.Fatalf("stdout cells: %+v %v", out, err)
	}
	if !strings.Contains(stderr.String(), "slots: full=0 clips=1") {
		t.Errorf("summary missing: %s", stderr.String())
	}
}
