package manifest

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/michaellady/longform-pipeline/internal/gates"
)

// TestMatchCellsIdentityRule pins the rule seal checks and opus schedule
// --job skips by: same clip and platform (a label stands in for a missing
// platform), an id, no error, each post used by one cell only, parked cells
// never matched.
func TestMatchCellsIdentityRule(t *testing.T) {
	m := &Manifest{SchemaVersion: SchemaVersion, Clips: []Clip{}}
	for _, l := range []ScheduleLine{
		{Cell: gates.Cell{ClipID: "A", Platform: "youtube"}, Response: json.RawMessage(`{"data":{"scheduleId":"s-a-yt"}}`)},
		{Cell: gates.Cell{ClipID: "A", Platform: "instagram"}, Error: "HTTP 400"},
		{Cell: gates.Cell{ClipID: "S", Platform: "facebook"}, Transport: TransportBuffer, Response: json.RawMessage(`{"ok":true}`)}, // Buffer line without post_id
	} {
		if err := m.AppendScheduleLine(l); err != nil {
			t.Fatal(err)
		}
	}
	// A post appended before posts carried platform: only the label names it.
	m.EnsureClip(Clip{ClipID: "L"})
	if err := m.AppendPost(ScheduledPost{ClipID: "L", Label: "YOUTUBE EVC", ScheduleID: "s-l"}); err != nil {
		t.Fatal(err)
	}
	cells := []gates.Cell{
		{ClipID: "A", Platform: "youtube", Label: "clip 1"},
		{ClipID: "A", Platform: "youtube", Label: "clip 1 again"}, // the one post is already used
		{ClipID: "A", Platform: "instagram"},
		{ClipID: "S", Platform: "facebook"},
		{ClipID: "L", Platform: "youtube"},
		{ClipID: "A", Platform: "youtube", Status: gates.StatusHeld},
		{ClipID: "Z", Platform: "youtube"},
	}
	got := m.MatchCells(cells)
	if len(got) != len(cells) {
		t.Fatalf("want one result per cell, got %d", len(got))
	}
	id := func(r CellMatch) string {
		if r.Post == nil {
			return ""
		}
		return r.Post.ScheduleID
	}
	if id(got[0]) != "s-a-yt" {
		t.Errorf("cell 0: %+v", got[0])
	}
	if got[1].Post != nil {
		t.Errorf("a post satisfies one cell only: %+v", got[1])
	}
	if got[2].Post != nil || got[2].Errored != "HTTP 400" {
		t.Errorf("a failed call is not a post: %+v", got[2])
	}
	if got[3].Post != nil || !got[3].NoID {
		t.Errorf("a post without an id is not a post: %+v", got[3])
	}
	if id(got[4]) != "s-l" {
		t.Errorf("label platform fallback: %+v", got[4])
	}
	if !got[5].Parked || got[5].Post != nil {
		t.Errorf("parked cell: %+v", got[5])
	}
	if got[6].Post != nil || got[6].Errored != "" || got[6].NoID {
		t.Errorf("unknown clip: %+v", got[6])
	}
}

// TestAppendPostIgnoresRecordedIDs: re-appending a line whose schedule id or
// Buffer post_id is already recorded adds nothing and exits 0; a failed
// line (no id) and a new id still append.
func TestAppendPostIgnoresRecordedIDs(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"init", "--job", dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit %d: %s", code, stderr.String())
	}
	first := strings.Join([]string{
		`{"cell":{"kind":"clip","clip_id":"c1","platform":"youtube","label":"clip 1"},"schedule_id":"sched-1","response":{"data":{"scheduleId":"sched-1"}}}`,
		`{"cell":{"kind":"static","clip_id":"s1","platform":"instagram","label":"static s1 (quote)"},"response":{"post":{"id":"b-9"}},"transport":"buffer","post_id":"b-9"}`,
		`{"cell":{"kind":"clip","clip_id":"c1","platform":"instagram","label":"clip 1"},"response":null,"error":"HTTP 400"}`,
	}, "\n")
	if code := Main([]string{"append-post", "--job", dir}, strings.NewReader(first), &stdout, &stderr); code != 0 {
		t.Fatalf("first append exit %d: %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "duplicate") {
		t.Fatalf("first append reported a duplicate: %s", stderr.String())
	}

	// The same three lines again, plus one line duplicated inside the input
	// (response id only) and one new id.
	again := first + "\n" +
		`{"cell":{"kind":"clip","clip_id":"c2","platform":"youtube","label":"clip 2"},"response":{"data":{"scheduleId":"sched-2"}}}` + "\n" +
		`{"cell":{"kind":"clip","clip_id":"c2","platform":"youtube","label":"clip 2"},"response":{"data":{"scheduleId":"sched-2"}}}`
	stderr.Reset()
	if code := Main([]string{"append-post", "--job", dir}, strings.NewReader(again), &stdout, &stderr); code != 0 {
		t.Fatalf("second append exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{"append-post: duplicate sched-1 ignored\n", "append-post: duplicate b-9 ignored\n", "append-post: duplicate sched-2 ignored\n", "manifest: appended 2 posts (5 total)"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
		}
	}
	m, err := Load(PathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	// c1: youtube once + the failed instagram attempt twice (no id, so it is
	// recorded each time, as before); s1 once; c2 once.
	if n := len(m.Find("c1").ScheduledPosts); n != 3 {
		t.Errorf("c1 posts = %d, want 3", n)
	}
	if n := len(m.Find("s1").ScheduledPosts); n != 1 {
		t.Errorf("s1 posts = %d, want 1", n)
	}
	if n := len(m.Find("c2").ScheduledPosts); n != 1 {
		t.Errorf("c2 posts = %d, want 1", n)
	}
	if ids := m.ScheduleIDs(); strings.Join(ids, ",") != "sched-1,b-9,sched-2" {
		t.Errorf("schedule ids = %v", ids)
	}
}

// recordingReader stands in for `opus schedule --job | append-post`: before
// it hands append-post its first byte, the writer records the post in the
// manifest itself (as opus schedule does after each POST).
type recordingReader struct {
	t      *testing.T
	path   string
	line   ScheduleLine
	r      io.Reader
	before bool
}

func (rr *recordingReader) Read(p []byte) (int, error) {
	if !rr.before {
		rr.before = true
		if added, err := RecordLine(rr.path, rr.line); err != nil || !added {
			rr.t.Fatalf("RecordLine: %v %v", added, err)
		}
	}
	return rr.r.Read(p)
}

// TestAppendPostRereadsManifestAfterInput: append-post reads the manifest
// again after its input ends, so posts opus schedule recorded while its
// stdout was being piped in are kept (not overwritten by a stale copy) and
// their lines are reported as duplicates.
func TestAppendPostRereadsManifestAfterInput(t *testing.T) {
	dir := t.TempDir()
	path := PathIn(dir)
	if _, err := Init(path, InitOptions{}); err != nil {
		t.Fatal(err)
	}
	raw := `{"cell":{"kind":"clip","clip_id":"c1","platform":"youtube","label":"clip 1"},"schedule_id":"sched-1","response":{"data":{"scheduleId":"sched-1"}}}`
	var l ScheduleLine
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatal(err)
	}
	in := &recordingReader{t: t, path: path, line: l, r: strings.NewReader(raw + "\n")}
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"append-post", "--job", dir}, in, &stdout, &stderr); code != 0 {
		t.Fatalf("append-post exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "append-post: duplicate sched-1 ignored") || !strings.Contains(stderr.String(), "appended 0 posts (1 total)") {
		t.Fatalf("stderr:\n%s", stderr.String())
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if ids := m.ScheduleIDs(); len(ids) != 1 || ids[0] != "sched-1" {
		t.Fatalf("the post recorded during the pipe was lost or doubled: %v", ids)
	}
}
