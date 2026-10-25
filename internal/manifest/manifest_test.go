package manifest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
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

// TestAppendLineIDsTransportAndProject: an explicit post_id (a Buffer line)
// wins, `opus schedule`'s top-level schedule_id is a fallback, the cell's
// project id is kept, and a failed line records no id.
func TestAppendLineIDsTransportAndProject(t *testing.T) {
	m := &Manifest{SchemaVersion: SchemaVersion, Clips: []Clip{}}
	lines := `{"cell":{"kind":"static","clip_id":"s1","platform":"instagram","label":"static s1 (quote)"},"response":{"post":{"id":"b-77"}},"transport":"buffer","post_id":"b-77"}
{"cell":{"kind":"static","clip_id":"s1","platform":"facebook","label":"static s1 (quote)"},"response":{"ok":true},"transport":"buffer"}
{"cell":{"kind":"clip","clip_id":"c1","project_id":"P9","platform":"youtube","label":"clip 1"},"schedule_id":"sched-top","response":{"weird":true}}
{"cell":{"kind":"clip","clip_id":"c1","project_id":"P9","platform":"instagram","label":"clip 1"},"schedule_id":"ignored","error":"HTTP 400"}`
	if n, err := appendLines(m, strings.NewReader(lines)); err != nil || n != 4 {
		t.Fatalf("append: %d %v", n, err)
	}
	s1, c1 := m.Find("s1").ScheduledPosts, m.Find("c1").ScheduledPosts
	if s1[0].ScheduleID != "b-77" || s1[0].Transport != TransportBuffer || s1[0].IsOpusClip() {
		t.Errorf("buffer post = %+v", s1[0])
	}
	if s1[1].ScheduleID != "" {
		t.Errorf("a Buffer line without post_id must not invent an id: %+v", s1[1])
	}
	if c1[0].ScheduleID != "sched-top" || c1[0].ProjectID != "P9" || !c1[0].IsOpusClip() {
		t.Errorf("opus post = %+v", c1[0])
	}
	if c1[1].ScheduleID != "" || c1[1].Error == "" {
		t.Errorf("failed post = %+v", c1[1])
	}
}

// TestExpireDaysDefaultsToProfile: without --days, expire reads the job's
// profile backlog_days; --days 0 is accepted; --days is required without --job.
func TestExpireDaysDefaultsToProfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LONGFORM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("LONGFORM_CONFIG_DIR", filepath.Join(root, "config"))
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	j, err := model.NewJob("evc", strings.Repeat("e", 64), nil, created)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(model.ProfilesDir(), 0o755)
	os.WriteFile(model.ProfilePath("evc"), []byte(`{"name":"evc","inbox":"/tmp/x","orientation":"portrait","backlog_days":10}`), 0o644)
	dir := j.Dir()
	var stdout, stderr bytes.Buffer
	Main([]string{"init", "--job", dir}, nil, &stdout, &stderr)
	Main([]string{"ensure-clip", "--job", dir, "--clip-id", "h1", "--status", "held"}, nil, &stdout, &stderr)

	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"expire", "--job", dir, "--now", "2026-10-10T00:00:00Z"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "older than 10 days (1 still held)") {
		t.Fatalf("day 9 should expire nothing under backlog_days 10: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"expire", "--job", dir, "--now", "2026-10-11T00:00:01Z"}, nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"h1"`) {
		t.Fatalf("day 10 should expire h1: exit %d %s %s", code, stdout.String(), stderr.String())
	}
	if code := Main([]string{"expire", "--manifest", PathIn(dir)}, nil, &stdout, &stderr); code != 64 {
		t.Fatalf("no --job and no --days: exit %d, want 64", code)
	}
	if code := Main([]string{"expire", "--manifest", PathIn(dir), "--days", "0"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("--days 0: exit %d %s", code, stderr.String())
	}
}
