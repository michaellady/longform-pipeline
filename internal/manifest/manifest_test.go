package manifest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const opusResp = `{"data":{"scheduleId":"1779159097402OmCL-FACEBOOK_PAGE","postId":"p1","hasConflict":false,"publishAt":"2026-05-19T16:00:00.000Z"}}`
const conflictResp = `{"data":{"scheduleId":"sched-2","hasConflict":true}}`

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := PathIn(dir)
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	wrote, err := Init(path, InitOptions{ProjectID: "P1", Now: now, Source: SourceVideo{ID: "vid", Title: "T", DurationSeconds: 1200, Profile: "evc"}})
	if err != nil || !wrote {
		t.Fatalf("init: %v %v", wrote, err)
	}
	if wrote, _ := Init(path, InitOptions{ProjectID: "OTHER"}); wrote {
		t.Fatal("init must be idempotent")
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != "1" || m.ProjectID != "P1" || m.SourceVideo.Profile != "evc" || m.SourceVideo.DurationSeconds != 1200 {
		t.Fatalf("shape: %+v", m)
	}
	score := 99.0
	if !m.EnsureClip(Clip{ClipID: "c1", Title: "one", Score: &score, DurationSec: 28}) || m.EnsureClip(Clip{ClipID: "c1"}) {
		t.Fatal("ensure-clip idempotency")
	}
	m.EnsureClip(Clip{ClipID: "c2", Title: "two", Status: StatusHeld, CuratedAt: "2026-09-01T00:00:00Z"})
	if err := m.AppendPost(ScheduledPost{ClipID: "c1", Label: "FACEBOOK_PAGE EVC", Platform: "facebook", ScheduledAtUTC: "2026-05-19T16:00:00Z", APIResponse: json.RawMessage(opusResp)}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendPost(ScheduledPost{ClipID: "c1", Label: "YOUTUBE EVC", ScheduledAtUTC: "2026-05-19T16:00:00Z", APIResponse: json.RawMessage(conflictResp)}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendPost(ScheduledPost{ClipID: "c1", Label: "THREADS x", APIResponse: json.RawMessage("not json")}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendPost(ScheduledPost{ClipID: "nope"}); err == nil {
		t.Fatal("append to unknown clip must fail")
	}
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	m2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m2.CountScheduled() != 3 {
		t.Fatalf("count %d", m2.CountScheduled())
	}
	c := m2.Find("c1")
	if c.Status != StatusPost || *c.Score != 99 || c.ScheduledPosts[0].ScheduleID != "1779159097402OmCL-FACEBOOK_PAGE" || c.ScheduledPosts[0].ClipID != "c1" {
		t.Fatalf("clip c1: %+v", c)
	}
	// Verbatim means the same JSON document (MarshalIndent re-indents nested
	// raw messages; jq did the same in the bash original).
	var want, got bytes.Buffer
	json.Compact(&want, []byte(opusResp))
	json.Compact(&got, c.ScheduledPosts[0].APIResponse)
	if got.String() != want.String() {
		t.Errorf("api_response must be verbatim, got %s", got.String())
	}
	if !strings.Contains(string(c.ScheduledPosts[2].APIResponse), `"raw"`) {
		t.Errorf("non-JSON response wrapped as raw, got %s", c.ScheduledPosts[2].APIResponse)
	}
	if ids := m2.ScheduleIDs(); len(ids) != 2 || ids[1] != "sched-2" {
		t.Errorf("schedule ids %v", ids)
	}
	if cf := m2.Conflicts(); len(cf) != 1 || cf[0].Label != "YOUTUBE EVC" || cf[0].ClipTitle != "one" {
		t.Errorf("conflicts %+v", cf)
	}
	if lc := m2.ListByChannel("facebook_page"); len(lc) != 1 || lc[0].ClipID != "c1" {
		t.Errorf("list-by-channel %+v", lc)
	}
	// Expire: c2 is held and curated 50 days before now; c1 is post.
	expired := m2.Expire(25, time.Time{}, now)
	if len(expired) != 1 || expired[0].ClipID != "c2" || m2.Find("c2").Status != StatusExpired {
		t.Errorf("expire %+v", expired)
	}
	if again := m2.Expire(25, time.Time{}, now); len(again) != 0 {
		t.Errorf("expire must be idempotent, got %+v", again)
	}
}

func TestAppendScheduleLines_FallbackAndMain(t *testing.T) {
	dir := t.TempDir()
	path := PathIn(dir)
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"init", "--job", dir, "--project", "P1", "--profile", "evc"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit %d: %s", code, stderr.String())
	}
	lines := strings.Join([]string{
		`{"cell":{"kind":"clip","clip_id":"c1","platform":"facebook","label":"FACEBOOK_PAGE EVC","account_id":"acc","sub_account_id":"sub","title":"t","description":"d","duration_sec":28,"publish_at":"2026-05-19T16:00:00Z"},"response":` + opusResp + `}`,
		``,
		`{"cell":{"kind":"clip","clip_id":"c1","platform":"youtube","label":"YOUTUBE EVC","publish_at":"2026-05-19T16:00:00Z"},"response":null,"error":"429 rate limited"}`,
	}, "\n")
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"append-post", "--job", dir}, strings.NewReader(lines), &stdout, &stderr); code != 0 {
		t.Fatalf("append-post exit %d: %s", code, stderr.String())
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c := m.Find("c1")
	if c == nil || c.Title != "t" || c.DurationSec != 28 || len(c.ScheduledPosts) != 2 {
		t.Fatalf("clip from schedule line: %+v", c)
	}
	if p := c.ScheduledPosts[0]; p.ScheduleID != "1779159097402OmCL-FACEBOOK_PAGE" || p.AccountID != "acc" || p.SubAccountID != "sub" || p.Platform != "facebook" || p.ScheduledAtUTC != "2026-05-19T16:00:00Z" {
		t.Errorf("post 0: %+v", p)
	}
	if p := c.ScheduledPosts[1]; p.Error != "429 rate limited" || p.ScheduleID != "" {
		t.Errorf("post 1: %+v", p)
	}

	stdout.Reset()
	Main([]string{"count-scheduled", "--job", dir}, nil, &stdout, &stderr)
	if strings.TrimSpace(stdout.String()) != "2" {
		t.Errorf("count-scheduled: %q", stdout.String())
	}
	stdout.Reset()
	Main([]string{"schedule-ids", "--job", dir}, nil, &stdout, &stderr)
	if strings.TrimSpace(stdout.String()) != "1779159097402OmCL-FACEBOOK_PAGE" {
		t.Errorf("schedule-ids: %q", stdout.String())
	}
	stdout.Reset()
	Main([]string{"list-by-channel", "youtube", "--job", dir}, nil, &stdout, &stderr)
	var lc []ChannelPost
	if json.Unmarshal(stdout.Bytes(), &lc) != nil || len(lc) != 1 || lc[0].ClipTitle != "t" {
		t.Errorf("list-by-channel: %s", stdout.String())
	}
	stdout.Reset()
	if code := Main([]string{"find-clip", "--job", dir, "--clip-id", "zz"}, nil, &stdout, &stderr); code != 65 {
		t.Errorf("find-clip missing exit %d", code)
	}
	if code := Main([]string{"bogus", "--job", dir}, nil, &stdout, &stderr); code != 64 {
		t.Errorf("unknown sub exit %d", code)
	}

	// expire falls back to clips.json curation time, then job.json created_at.
	Main([]string{"ensure-clip", "--job", dir, "--clip-id", "h1", "--status", "held"}, nil, &stdout, &stderr)
	os.WriteFile(filepath.Join(dir, "clips.json"), []byte(`{"curated_at":"2026-09-01T00:00:00Z","clips":[]}`), 0o644)
	stdout.Reset()
	if code := Main([]string{"expire", "--job", dir, "--days", "25", "--now", "2026-10-21T00:00:00Z"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("expire exit %d: %s", code, stderr.String())
	}
	var expired []Clip
	if json.Unmarshal(stdout.Bytes(), &expired) != nil || len(expired) != 1 || expired[0].ClipID != "h1" {
		t.Errorf("expire output: %s", stdout.String())
	}
	m, _ = Load(path)
	if m.Find("h1").Status != StatusExpired {
		t.Errorf("expired status not persisted")
	}
	// No temp files left behind by atomic writes.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
}
