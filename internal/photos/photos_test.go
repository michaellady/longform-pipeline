package photos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

const album = "longform-evc"

func setupEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LONGFORM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("LONGFORM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("LONGFORM_LOG_DIR", filepath.Join(root, "logs"))
	t.Setenv("LONGFORM_SLACK_WEBHOOK", "")
	return root
}

func profile() *model.Profile {
	return &model.Profile{Name: "evc", Photos: model.PhotosConfig{Enabled: true, Album: album}}
}

func stitchProfile() *model.Profile {
	p := profile()
	p.Stitch = model.StitchConfig{Enabled: true, SessionWindowMin: 90}
	return p
}

// original writes a file standing in for an original inside the Photos
// library and returns its path.
func original(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "Photos Library.photoslibrary", "originals", "A", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type feedCall struct {
	files    []FeedFile
	contents []string // bytes of each file at call time
}

// harness records Feed, Notify and log calls; feed decides what Feed returns
// (default: every file lands in job "job-<uuid>").
type harness struct {
	fake    *FakeOsxphotos
	feeds   []feedCall
	notices []string
	logs    []string
	feed    func(files []FeedFile) (map[string]FeedResult, error)
	now     time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	setupEnv(t)
	return &harness{
		fake: NewFakeOsxphotos(t),
		feed: func(files []FeedFile) (map[string]FeedResult, error) {
			m := map[string]FeedResult{}
			for _, f := range files {
				m[f.Path] = FeedResult{JobID: "job-" + f.Extra["uuid"].(string)}
			}
			return m, nil
		},
		now: time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC),
	}
}

func (h *harness) opts(t *testing.T) Options {
	return Options{
		Client: Client{Bin: h.fake.Bin},
		Feed: func(files []FeedFile) (map[string]FeedResult, error) {
			c := feedCall{files: files}
			for _, f := range files {
				b, err := os.ReadFile(f.Path)
				if err != nil {
					t.Errorf("Feed got unreadable %s: %v", f.Path, err)
				}
				c.contents = append(c.contents, string(b))
			}
			h.feeds = append(h.feeds, c)
			return h.feed(files)
		},
		Now: func() time.Time { return h.now },
		Logf: func(format string, args ...any) {
			t.Logf(format, args...)
			h.logs = append(h.logs, fmt.Sprintf(format, args...))
		},
		Notify: func(p *model.Profile, event, message string) { h.notices = append(h.notices, event+": "+message) },
	}
}

func (h *harness) pass(t *testing.T, p *model.Profile) Report {
	t.Helper()
	rep, err := Pass(context.Background(), p, h.opts(t))
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	return rep
}

func record(t *testing.T, uuid string) *Record {
	t.Helper()
	st, err := LoadState("evc")
	if err != nil {
		t.Fatal(err)
	}
	r := st.Assets[uuid]
	if r == nil {
		t.Fatalf("no state record for %s (have %v)", uuid, st.Assets)
	}
	return r
}

func incomingDir(uuid string) string {
	return filepath.Join(model.DataRoot(), "photos", "incoming", "evc", uuid)
}

func paths(files []FeedFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

// A ready original is cloned to incoming/<profile>/<uuid>/<original name>,
// staged with the asset's provenance, and left untouched in the library; the
// record becomes fed with the job id.
func TestPassFeedsReadyAsset(t *testing.T) {
	h := newHarness(t)
	src := original(t, "IMG_0001.MOV", "movie bytes")
	h.fake.SetAssets(t, FakeMovie("UUID-READY", "IMG_0001.MOV", src, 11))

	rep := h.pass(t, profile())

	if rep != (Report{Seen: 1, Fed: 1}) {
		t.Fatalf("report = %+v", rep)
	}
	if len(h.feeds) != 1 {
		t.Fatalf("Feed calls = %d", len(h.feeds))
	}
	want := filepath.Join(incomingDir("UUID-READY"), "IMG_0001.MOV")
	wantFiles := []FeedFile{{Path: want, Extra: map[string]any{
		"uuid": "UUID-READY", "album": album, "original_path": src,
		"original_filename": "IMG_0001.MOV", "date": "2026-10-20T09:15:00-07:00",
	}}}
	if !reflect.DeepEqual(h.feeds[0].files, wantFiles) {
		t.Errorf("Feed files = %#v\nwant %#v", h.feeds[0].files, wantFiles)
	}
	if h.feeds[0].contents[0] != "movie bytes" {
		t.Errorf("clone content = %q", h.feeds[0].contents[0])
	}
	if b, err := os.ReadFile(src); err != nil || string(b) != "movie bytes" {
		t.Errorf("library original must stay put: %q, %v", b, err)
	}
	if _, err := os.Stat(incomingDir("UUID-READY")); !os.IsNotExist(err) {
		t.Errorf("incoming dir should be cleaned after feeding: %v", err)
	}
	r := record(t, "UUID-READY")
	if r.Status != StatusFed || r.JobID != "job-UUID-READY" || !r.FedAt.Equal(h.now) || r.Incoming != want || r.Note != "" {
		t.Errorf("record = %+v", *r)
	}
	if r.OriginalFilename != "IMG_0001.MOV" || r.SizeBytes != 11 || !r.FirstSeen.Equal(h.now) {
		t.Errorf("record identity = %+v", *r)
	}
	if len(h.notices) != 0 {
		t.Errorf("no notice expected for a ready asset: %v", h.notices)
	}
	calls := h.fake.Calls(t)
	if len(calls) != 1 || !slices.Equal(calls[0], []string{"query", "--album", album, "--only-movies", "--json", "--mute"}) {
		t.Errorf("osxphotos calls = %v", calls)
	}
}

// Every ready asset of a pass goes to stage in ONE Feed call (so stage can
// group a session); a non-stitch profile never holds them back for a
// recording that is still syncing.
func TestPassFeedsAllReadyInOneCall(t *testing.T) {
	h := newHarness(t)
	h.fake.SetAssets(t,
		FakeMovie("UUID-A", "A.MOV", original(t, "A.MOV", "a"), 1),
		FakeMovie("UUID-CLOUD", "C.MOV", "", 1),
		FakeMovie("UUID-B", "B.MOV", original(t, "B.MOV", "b"), 1))
	h.feed = func(files []FeedFile) (map[string]FeedResult, error) {
		return map[string]FeedResult{files[0].Path: {JobID: "job-1"}, files[1].Path: {JobID: "job-2"}}, nil
	}

	rep := h.pass(t, profile())

	if rep != (Report{Seen: 3, Waiting: 1, Fed: 2}) {
		t.Fatalf("report = %+v", rep)
	}
	if len(h.feeds) != 1 {
		t.Fatalf("Feed calls = %d, want 1", len(h.feeds))
	}
	want := []string{filepath.Join(incomingDir("UUID-A"), "A.MOV"), filepath.Join(incomingDir("UUID-B"), "B.MOV")}
	if got := paths(h.feeds[0].files); !slices.Equal(got, want) || !slices.Equal(h.feeds[0].contents, []string{"a", "b"}) {
		t.Fatalf("Feed files = %v %v", got, h.feeds[0].contents)
	}
	if record(t, "UUID-A").JobID != "job-1" || record(t, "UUID-B").JobID != "job-2" {
		t.Errorf("job ids not taken per file: %+v %+v", *record(t, "UUID-A"), *record(t, "UUID-B"))
	}
	if r := record(t, "UUID-CLOUD"); r.Status != StatusWaiting || len(h.notices) != 1 {
		t.Errorf("waiting record %+v, notices %v", *r, h.notices)
	}
}

// A stitch profile holds the ready part of the album back while any
// recording is still syncing (one class, several segments: staging the first
// half alone would split the session), keeps the clones, persists the
// notice, and feeds the whole batch once the last segment lands.
func TestPassStitchDefersWhileSyncing(t *testing.T) {
	h := newHarness(t)
	partA := original(t, "PART_A.MOV", "first half")
	h.fake.SetAssets(t,
		FakeMovie("UUID-A", "PART_A.MOV", partA, 10),
		FakeMovie("UUID-B", "PART_B.MOV", "", 2_000_000_000))

	for i := range 2 {
		h.logs = nil
		rep := h.pass(t, stitchProfile())
		if rep != (Report{Seen: 2, Waiting: 1, Deferred: 1}) {
			t.Fatalf("pass %d report = %+v", i+1, rep)
		}
		wantLog := "photos evc: 1 ready, 1 still syncing; deferring stitch grouping until the batch is on this Mac"
		if !slices.Contains(h.logs, wantLog) {
			t.Errorf("pass %d: no deferral log in %q", i+1, h.logs)
		}
	}
	if len(h.feeds) != 0 {
		t.Fatalf("Feed ran while the batch was incomplete: %+v", h.feeds)
	}
	if len(h.notices) != 1 || !strings.Contains(h.notices[0], "PART_B.MOV (2.0 GB)") {
		t.Fatalf("notices = %v, want exactly one for PART_B", h.notices)
	}
	clone := filepath.Join(incomingDir("UUID-A"), "PART_A.MOV")
	if b, err := os.ReadFile(clone); err != nil || string(b) != "first half" {
		t.Errorf("held-back clone should stay: %q, %v", b, err)
	}
	if r := record(t, "UUID-A"); r.Status != StatusWaiting || r.Incoming != clone || r.Notified {
		t.Errorf("held-back record = %+v", *r)
	}
	if r := record(t, "UUID-B"); r.Status != StatusWaiting || !r.Notified {
		t.Errorf("syncing record (notified flag must persist) = %+v", *r)
	}

	h.fake.SetAssets(t,
		FakeMovie("UUID-A", "PART_A.MOV", partA, 10),
		FakeMovie("UUID-B", "PART_B.MOV", original(t, "PART_B.MOV", "second half"), 11))
	h.feed = func(files []FeedFile) (map[string]FeedResult, error) {
		m := map[string]FeedResult{}
		for _, f := range files {
			m[f.Path] = FeedResult{JobID: "job-stitched"}
		}
		return m, nil
	}
	rep := h.pass(t, stitchProfile())
	if rep != (Report{Seen: 2, Fed: 2}) {
		t.Fatalf("arrival report = %+v", rep)
	}
	if len(h.feeds) != 1 || !slices.Equal(h.feeds[0].contents, []string{"first half", "second half"}) {
		t.Fatalf("want one Feed with both halves, got %+v", h.feeds)
	}
	if record(t, "UUID-A").JobID != "job-stitched" || record(t, "UUID-B").JobID != "job-stitched" {
		t.Errorf("both halves should record the stitched job")
	}
	if len(h.notices) != 1 {
		t.Errorf("no further notice expected: %v", h.notices)
	}
}

// A fed asset is never fed again; later passes count it as done.
func TestPassDoesNotRefeedFedAsset(t *testing.T) {
	h := newHarness(t)
	src := original(t, "IMG_0001.MOV", "movie bytes")
	h.fake.SetAssets(t, FakeMovie("UUID-READY", "IMG_0001.MOV", src, 11))
	h.pass(t, profile())

	later := h.now.Add(5 * time.Minute)
	h.now = later
	rep := h.pass(t, profile())

	if rep != (Report{Seen: 1, Done: 1}) {
		t.Fatalf("second report = %+v", rep)
	}
	if len(h.feeds) != 1 {
		t.Fatalf("Feed calls = %d, want 1", len(h.feeds))
	}
	r := record(t, "UUID-READY")
	if !r.LastSeen.Equal(later) || r.FedAt.Equal(later) || r.JobID != "job-UUID-READY" {
		t.Errorf("record after second pass = %+v", *r)
	}
}

// An original still in iCloud waits, notifies once across passes, and is fed
// on the pass where it lands on this Mac.
func TestPassMissingAssetWaitsNotifiesOnceThenFeeds(t *testing.T) {
	h := newHarness(t)
	h.fake.SetAssets(t, FakeMovie("UUID-CLOUD", "IMG_0002.MOV", "", 3_400_000_000))

	for i := range 2 {
		rep := h.pass(t, profile())
		if rep != (Report{Seen: 1, Waiting: 1}) {
			t.Fatalf("pass %d report = %+v", i+1, rep)
		}
	}
	if len(h.feeds) != 0 {
		t.Fatalf("Feed must not run for a missing original: %d", len(h.feeds))
	}
	if len(h.notices) != 1 {
		t.Fatalf("notices = %v, want exactly one", h.notices)
	}
	if want := "waiting: Waiting for IMG_0002.MOV (3.4 GB)"; !strings.HasPrefix(h.notices[0], want) {
		t.Errorf("notice = %q, want prefix %q", h.notices[0], want)
	}
	r := record(t, "UUID-CLOUD")
	if r.Status != StatusWaiting || !r.Notified || r.JobID != "" {
		t.Errorf("record = %+v", *r)
	}
	for _, c := range h.fake.Calls(t) {
		if c[0] != "query" {
			t.Errorf("download_missing is off; unexpected osxphotos %v", c)
		}
	}

	src := original(t, "IMG_0002.MOV", "now on disk")
	h.fake.SetAssets(t, FakeMovie("UUID-CLOUD", "IMG_0002.MOV", src, 11))
	rep := h.pass(t, profile())
	if rep != (Report{Seen: 1, Fed: 1}) {
		t.Fatalf("arrival report = %+v", rep)
	}
	if len(h.feeds) != 1 || h.feeds[0].contents[0] != "now on disk" {
		t.Fatalf("feeds = %+v", h.feeds)
	}
	if len(h.notices) != 1 {
		t.Errorf("no further notice expected: %v", h.notices)
	}
	if r := record(t, "UUID-CLOUD"); r.Status != StatusFed {
		t.Errorf("record = %+v", *r)
	}
}

// path set but the file gone (library moved, original purged) is not ready.
func TestPassPathWithoutFileWaits(t *testing.T) {
	h := newHarness(t)
	h.fake.SetAssets(t, FakeMovie("UUID-GONE", "IMG_0003.MOV", filepath.Join(t.TempDir(), "gone.mov"), 5))
	if rep := h.pass(t, profile()); rep != (Report{Seen: 1, Waiting: 1}) {
		t.Fatalf("report = %+v", rep)
	}
	if len(h.feeds) != 0 {
		t.Fatal("Feed ran for a missing file")
	}
}

// A true duplicate (no job, no error: its bytes are already in a job) still
// settles that asset as fed, with a note, and removes its clone; the other
// assets of the batch keep their jobs.
func TestPassDuplicateMarksFedAndRemovesClone(t *testing.T) {
	h := newHarness(t)
	h.fake.SetAssets(t,
		FakeMovie("UUID-DUP", "IMG_0004.MOV", original(t, "IMG_0004.MOV", "dup bytes"), 9),
		FakeMovie("UUID-NEW", "IMG_0005.MOV", original(t, "IMG_0005.MOV", "new bytes"), 9))
	h.feed = func(files []FeedFile) (map[string]FeedResult, error) {
		return map[string]FeedResult{files[0].Path: {}, files[1].Path: {JobID: "job-new"}}, nil
	}

	rep := h.pass(t, profile())

	if rep != (Report{Seen: 2, Fed: 2}) {
		t.Fatalf("report = %+v", rep)
	}
	if _, err := os.Stat(incomingDir("UUID-DUP")); !os.IsNotExist(err) {
		t.Errorf("clone dir should be removed: %v", err)
	}
	r := record(t, "UUID-DUP")
	if r.Status != StatusFed || r.JobID != "" || !strings.Contains(r.Note, "duplicate") {
		t.Errorf("record = %+v", *r)
	}
	if r := record(t, "UUID-NEW"); r.Status != StatusFed || r.JobID != "job-new" || r.Note != "" {
		t.Errorf("batch neighbour = %+v", *r)
	}
	if rep := h.pass(t, profile()); rep != (Report{Seen: 2, Done: 2}) || len(h.feeds) != 1 {
		t.Errorf("duplicate must not be retried: %+v, %d feeds", rep, len(h.feeds))
	}
}

// A file stage made nothing of for a reason (unreadable video, failed stage
// group) — or did not report at all — fails at once with that reason, keeps
// its clone for inspection, and notifies "failed" exactly once.
func TestPassStageRejectFailsAndKeepsClone(t *testing.T) {
	h := newHarness(t)
	h.fake.SetAssets(t,
		FakeMovie("UUID-BAD", "IMG_0010.MOV", original(t, "IMG_0010.MOV", "junk"), 4),
		FakeMovie("UUID-LOST", "IMG_0011.MOV", original(t, "IMG_0011.MOV", "lost"), 4),
		FakeMovie("UUID-OK", "IMG_0012.MOV", original(t, "IMG_0012.MOV", "fine"), 4))
	reason := "stage evc: IMG_0010.MOV is not a readable video (ffprobe: moov atom not found); skipping"
	h.feed = func(files []FeedFile) (map[string]FeedResult, error) {
		return map[string]FeedResult{files[0].Path: {Err: reason}, files[2].Path: {JobID: "job-ok"}}, nil
	}

	rep := h.pass(t, profile())

	if rep != (Report{Seen: 3, Fed: 1, Failed: 2}) {
		t.Fatalf("report = %+v", rep)
	}
	for uuid, note := range map[string]string{"UUID-BAD": reason, "UUID-LOST": "stage reported nothing for this file"} {
		r := record(t, uuid)
		if r.Status != StatusFailed || r.Note != note || r.JobID != "" {
			t.Errorf("%s record = %+v", uuid, *r)
		}
		if _, err := os.Stat(r.Incoming); err != nil {
			t.Errorf("%s clone must be kept for inspection: %v", uuid, err)
		}
	}
	want := []string{
		"failed: Could not stage IMG_0010.MOV from the Photos album: " + reason,
		"failed: Could not stage IMG_0011.MOV from the Photos album: stage reported nothing for this file",
	}
	if !slices.Equal(h.notices, want) {
		t.Errorf("notices = %q\nwant %q", h.notices, want)
	}
	if rep := h.pass(t, profile()); rep != (Report{Seen: 3, Done: 1, Failed: 2}) || len(h.feeds) != 1 || len(h.notices) != 2 {
		t.Errorf("failed assets must not be retried or re-notified: %+v, %d feeds, %v", rep, len(h.feeds), h.notices)
	}
}

// A Feed error (stage could not run at all) keeps the batch waiting with the
// reason and an attempt count; the third failed attempt fails the records
// and notifies once each.
func TestPassFeedErrorRetriesThenFails(t *testing.T) {
	h := newHarness(t)
	h.fake.SetAssets(t,
		FakeMovie("UUID-F1", "IMG_0005.MOV", original(t, "IMG_0005.MOV", "bytes"), 5),
		FakeMovie("UUID-F2", "IMG_0006.MOV", original(t, "IMG_0006.MOV", "bytes2"), 6))
	h.feed = func([]FeedFile) (map[string]FeedResult, error) { return nil, errors.New("index: disk full") }

	for attempt := 1; attempt < MaxFeedAttempts; attempt++ {
		if rep := h.pass(t, profile()); rep != (Report{Seen: 2, Waiting: 2}) {
			t.Fatalf("attempt %d report = %+v", attempt, rep)
		}
		for _, u := range []string{"UUID-F1", "UUID-F2"} {
			r := record(t, u)
			if r.Status != StatusWaiting || r.Attempts != attempt || r.Note != fmt.Sprintf("stage attempt %d/3: index: disk full", attempt) {
				t.Errorf("attempt %d %s record = %+v", attempt, u, *r)
			}
		}
	}
	if len(h.notices) != 0 {
		t.Fatalf("no notice before giving up: %v", h.notices)
	}

	if rep := h.pass(t, profile()); rep != (Report{Seen: 2, Failed: 2}) {
		t.Fatalf("final attempt report = %+v", rep)
	}
	for _, u := range []string{"UUID-F1", "UUID-F2"} {
		if r := record(t, u); r.Status != StatusFailed || r.Attempts != MaxFeedAttempts || r.Note != "stage attempt 3/3: index: disk full" {
			t.Errorf("%s record = %+v", u, *r)
		}
	}
	if len(h.notices) != 2 || !strings.Contains(h.notices[0], "IMG_0005.MOV") || !strings.Contains(h.notices[1], "IMG_0006.MOV") {
		t.Errorf("notices = %v", h.notices)
	}
	if rep := h.pass(t, profile()); rep != (Report{Seen: 2, Failed: 2}) || len(h.feeds) != MaxFeedAttempts {
		t.Errorf("failed assets must not be retried: %+v, %d feeds", rep, len(h.feeds))
	}

	// A batch that recovers before the limit is staged normally.
	h2 := newHarness(t)
	h2.fake.SetAssets(t, FakeMovie("UUID-R", "IMG_0007.MOV", original(t, "IMG_0007.MOV", "r"), 1))
	ok := h2.feed
	h2.feed = func([]FeedFile) (map[string]FeedResult, error) { return nil, errors.New("index: busy") }
	h2.pass(t, profile())
	h2.feed = ok
	if rep := h2.pass(t, profile()); rep != (Report{Seen: 1, Fed: 1}) {
		t.Fatalf("recovered report = %+v", rep)
	}
	if r := record(t, "UUID-R"); r.Status != StatusFed || r.Attempts != 1 || r.Note != "" || r.JobID != "job-UUID-R" {
		t.Errorf("recovered record = %+v", *r)
	}
}

// Photos (not movies) and assets without a uuid are not the feed's business.
func TestPassIgnoresNonMoviesAndEmptyUUID(t *testing.T) {
	h := newHarness(t)
	photo := FakeMovie("UUID-PHOTO", "IMG_0006.HEIC", original(t, "IMG_0006.HEIC", "still"), 5)
	photo["ismovie"] = false
	noUUID := FakeMovie("", "IMG_0007.MOV", original(t, "IMG_0007.MOV", "x"), 1)
	h.fake.SetAssets(t, photo, noUUID)

	if rep := h.pass(t, profile()); rep != (Report{}) {
		t.Fatalf("report = %+v", rep)
	}
	if len(h.feeds) != 0 || len(h.notices) != 0 {
		t.Fatalf("feeds %d notices %v", len(h.feeds), h.notices)
	}
	st, err := LoadState("evc")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Assets) != 0 || st.Album != album {
		t.Errorf("state = %+v", st)
	}
}

// download_missing exports the original through osxphotos into the asset's
// incoming dir (cleared first) and feeds that file.
func TestPassDownloadsMissingOriginal(t *testing.T) {
	h := newHarness(t)
	h.fake.SetAssets(t, FakeMovie("UUID-DL", "IMG_0008.MOV", "", 10))
	h.fake.ExportFrom(t, "UUID-DL", original(t, "export-src.mov", "downloaded"), "IMG_0008.MOV")
	stale := filepath.Join(incomingDir("UUID-DL"), "IMG_0008 (1).MOV")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := profile()
	p.Photos.DownloadMissing = true
	p.Photos.Library = "/Volumes/Photos/Other.photoslibrary"

	rep := h.pass(t, p)

	if rep != (Report{Seen: 1, Fed: 1}) {
		t.Fatalf("report = %+v", rep)
	}
	want := filepath.Join(incomingDir("UUID-DL"), "IMG_0008.MOV")
	if len(h.feeds) != 1 || !slices.Equal(paths(h.feeds[0].files), []string{want}) || h.feeds[0].contents[0] != "downloaded" {
		t.Fatalf("feeds = %+v", h.feeds)
	}
	if got := h.feeds[0].files[0].Extra["original_path"]; got != "" {
		t.Errorf("original_path = %q; a downloaded export is not a library path", got)
	}
	if len(h.notices) != 0 {
		t.Errorf("notices = %v", h.notices)
	}
	if _, err := os.Stat(incomingDir("UUID-DL")); !os.IsNotExist(err) {
		t.Errorf("export dir (file, stale copy, export db) should be cleaned: %v", err)
	}
	calls := h.fake.Calls(t)
	if len(calls) != 2 {
		t.Fatalf("calls = %v", calls)
	}
	wantExport := []string{"export", incomingDir("UUID-DL"), "--uuid", "UUID-DL", "--download-missing", "--use-photokit", "--skip-edited", "--no-progress", "--library", p.Photos.Library}
	if !slices.Equal(calls[1], wantExport) {
		t.Errorf("export argv = %v\nwant %v", calls[1], wantExport)
	}
	if !slices.Equal(calls[0][len(calls[0])-2:], []string{"--library", p.Photos.Library}) {
		t.Errorf("query argv lacks --library: %v", calls[0])
	}
}

// A download held back for a stitch batch is reused on later passes, never
// downloaded twice, while the segment that failed to download is retried.
func TestPassReusesHeldBackDownload(t *testing.T) {
	h := newHarness(t)
	h.fake.SetAssets(t,
		FakeMovie("UUID-A", "PART_A.MOV", "", 10),
		FakeMovie("UUID-B", "PART_B.MOV", "", 11))
	h.fake.ExportFrom(t, "UUID-A", original(t, "a-src.mov", "first half"), "PART_A.MOV")
	p := stitchProfile()
	p.Photos.DownloadMissing = true

	for i := range 2 {
		if rep := h.pass(t, p); rep != (Report{Seen: 2, Waiting: 1, Deferred: 1}) {
			t.Fatalf("pass %d report = %+v", i+1, rep)
		}
	}
	exports := map[string]int{}
	for _, c := range h.fake.Calls(t) {
		if c[0] == "export" {
			exports[c[3]]++
		}
	}
	if exports["UUID-A"] != 1 || exports["UUID-B"] != 2 {
		t.Fatalf("export attempts = %v, want A once and B every pass", exports)
	}
	if r := record(t, "UUID-B"); !strings.Contains(r.Note, "nothing exported") || !r.Notified {
		t.Errorf("failed segment record = %+v", *r)
	}

	h.fake.ExportFrom(t, "UUID-B", original(t, "b-src.mov", "second half"), "PART_B.MOV")
	if rep := h.pass(t, p); rep != (Report{Seen: 2, Fed: 2}) {
		t.Fatalf("arrival report = %+v", rep)
	}
	if len(h.feeds) != 1 || !slices.Equal(h.feeds[0].contents, []string{"first half", "second half"}) {
		t.Fatalf("feeds = %+v", h.feeds)
	}
	if r := record(t, "UUID-B"); r.Note != "" {
		t.Errorf("a fed record keeps no stale download note: %+v", *r)
	}
}

// A failing download leaves the asset waiting with the reason noted, notifies
// once, and is retried on the next pass.
func TestPassDownloadFailureWaits(t *testing.T) {
	h := newHarness(t)
	h.fake.SetAssets(t, FakeMovie("UUID-DLF", "IMG_0009.MOV", "", 10))
	h.fake.Fail(t, "export", "PhotoKitAuthError: Could not get authorizaton to use Photos", 1)
	p := profile()
	p.Photos.DownloadMissing = true

	for i := range 2 {
		if rep := h.pass(t, p); rep != (Report{Seen: 1, Waiting: 1}) {
			t.Fatalf("pass %d report = %+v", i+1, rep)
		}
	}
	if len(h.feeds) != 0 || len(h.notices) != 1 {
		t.Fatalf("feeds %d notices %v", len(h.feeds), h.notices)
	}
	r := record(t, "UUID-DLF")
	if r.Status != StatusWaiting || !strings.HasPrefix(r.Note, "download: osxphotos:") || !strings.Contains(r.Note, "PhotoKitAuthError") {
		t.Errorf("record = %+v", *r)
	}
	exports := 0
	for _, c := range h.fake.Calls(t) {
		if c[0] == "export" {
			exports++
		}
	}
	if exports != 2 {
		t.Errorf("export attempts = %d, want one per pass", exports)
	}
}

// Download reports an export that wrote nothing (osxphotos exits 0 when a
// single photo fails) and ignores osxphotos' dot files.
func TestDownloadNothingExported(t *testing.T) {
	setupEnv(t)
	fake := NewFakeOsxphotos(t)
	dir := filepath.Join(t.TempDir(), "x")
	_, err := Client{Bin: fake.Bin}.Download(context.Background(), "U1", dir)
	if err == nil || !strings.Contains(err.Error(), "nothing exported") {
		t.Fatalf("err = %v", err)
	}
}

// A cancel (launchd SIGTERM) stops the pass: no asset after it is looked at,
// nothing is staged, and what was recorded is saved.
func TestPassStopsOnCancel(t *testing.T) {
	h := newHarness(t)
	h.fake.SetAssets(t,
		FakeMovie("UUID-A", "A.MOV", original(t, "A.MOV", "a"), 1),
		FakeMovie("UUID-CLOUD", "C.MOV", "", 1),
		FakeMovie("UUID-B", "B.MOV", original(t, "B.MOV", "b"), 1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := h.opts(t)
	notify := opts.Notify
	opts.Notify = func(p *model.Profile, event, message string) {
		notify(p, event, message)
		cancel() // SIGTERM arrives while the second asset is checked
	}

	rep, err := Pass(ctx, profile(), opts)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if rep != (Report{Seen: 2, Waiting: 1}) || len(h.feeds) != 0 {
		t.Fatalf("report = %+v, feeds %d", rep, len(h.feeds))
	}
	st, lerr := LoadState("evc")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if r := st.Assets["UUID-CLOUD"]; r == nil || !r.Notified {
		t.Errorf("notice before the cancel not saved: %+v", r)
	}
	if r := st.Assets["UUID-A"]; r == nil || r.Status != StatusWaiting {
		t.Errorf("ready asset must stay waiting for the next pass: %+v", r)
	}
	if _, ok := st.Assets["UUID-B"]; ok {
		t.Error("asset after the cancel must be left for the next pass")
	}

	// A cancel after the last asset still stages nothing.
	h2 := newHarness(t)
	h2.fake.SetAssets(t, FakeMovie("UUID-A", "A.MOV", original(t, "A.MOV", "a"), 1), FakeMovie("UUID-CLOUD", "C.MOV", "", 1))
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	opts2 := h2.opts(t)
	opts2.Notify = func(*model.Profile, string, string) { cancel2() }
	if _, err := Pass(ctx2, profile(), opts2); !errors.Is(err, context.Canceled) || len(h2.feeds) != 0 {
		t.Errorf("late cancel: err %v, feeds %d", err, len(h2.feeds))
	}
}

func TestPassDisabledOrNoAlbumIsNoop(t *testing.T) {
	h := newHarness(t)
	for _, p := range []*model.Profile{
		{Name: "evc"},
		{Name: "evc", Photos: model.PhotosConfig{Enabled: true}},
		{Name: "evc", Photos: model.PhotosConfig{Album: album}},
	} {
		if rep, err := Pass(context.Background(), p, h.opts(t)); err != nil || rep != (Report{}) {
			t.Errorf("%+v: %+v, %v", p.Photos, rep, err)
		}
	}
	if calls := h.fake.Calls(t); len(calls) != 0 {
		t.Errorf("osxphotos ran: %v", calls)
	}
	if _, err := Pass(context.Background(), profile(), Options{Client: Client{Bin: h.fake.Bin}}); err == nil || !strings.Contains(err.Error(), "Feed is required") {
		t.Errorf("missing Feed: %v", err)
	}
}

// osxphotos runs in the log dir, so its osxphotos_crash.log lands next to
// watch.log instead of "/" (launchd's cwd).
func TestClientRunsInLogDir(t *testing.T) {
	root := setupEnv(t)
	fake := NewFakeOsxphotos(t)
	fake.Fail(t, "query", "OSError: Error copying/x/database/Photos.sqlite to /tmp/y\n", 1)
	_, err := Client{Bin: fake.Bin}.List(context.Background(), album)
	logDir := filepath.Join(root, "logs")
	if !errors.Is(err, ErrNoAccess) || !strings.Contains(err.Error(), "osxphotos_crash.log in "+logDir) {
		t.Fatalf("err = %v", err)
	}
	cwd, err := os.ReadFile(filepath.Join(fake.dir, "cwd"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(logDir)
	if got, _ := filepath.EvalSymlinks(string(cwd)); got != want {
		t.Errorf("osxphotos cwd = %s, want %s", got, want)
	}
}

func TestStateRoundTrip(t *testing.T) {
	setupEnv(t)
	empty, err := LoadState("evc")
	if err != nil || len(empty.Assets) != 0 || empty.SchemaVersion != "1" {
		t.Fatalf("absent state = %+v, %v", empty, err)
	}
	now := time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC)
	st := &State{SchemaVersion: "1", Album: album, Assets: map[string]*Record{
		"U1": {UUID: "U1", OriginalFilename: "IMG_1.MOV", SizeBytes: 42, Status: StatusFed, FirstSeen: now, LastSeen: now.Add(time.Minute), FedAt: now, JobID: "j1", Incoming: "/x/IMG_1.MOV", Notified: true},
		"U2": {UUID: "U2", Status: StatusWaiting, FirstSeen: now, LastSeen: now, Note: "download: no"},
	}}
	if err := st.Save("evc"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(model.DataRoot(), "photos", "evc.json")); err != nil {
		t.Fatalf("state path: %v", err)
	}
	got, err := LoadState("evc")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, st) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, st)
	}
	if err := os.WriteFile(filepath.Join(Root(), "evc.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState("evc"); err == nil || !strings.Contains(err.Error(), "evc.json") {
		t.Errorf("corrupt state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(Root(), "evc.json"), []byte(`{"schema_version":"1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, err := LoadState("evc"); err != nil || st.Assets == nil {
		t.Errorf("null assets must load as an empty map: %+v, %v", st, err)
	}
}

// The real osxphotos fails this way when the caller lacks Full Disk Access:
// photosdb raises OSError("Error copying<path>/Photos.sqlite to <tmp>").
func TestListNoAccess(t *testing.T) {
	setupEnv(t)
	fake := NewFakeOsxphotos(t)
	fake.Fail(t, "query", "Traceback (most recent call last):\n  ...\nOSError: Error copying/Users/me/Pictures/Photos Library.photoslibrary/database/Photos.sqlite to /var/folders/x/Photos.sqlite\n", 1)
	c := Client{Bin: fake.Bin}

	if _, err := c.List(context.Background(), album); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("List err = %v, want ErrNoAccess", err)
	}
	h := &harness{fake: fake, feed: func([]FeedFile) (map[string]FeedResult, error) { return nil, nil }}
	if _, err := Pass(context.Background(), profile(), h.opts(t)); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("Pass err = %v, want ErrNoAccess", err)
	}
	if _, err := os.Stat(filepath.Join(Root(), "evc.json")); !os.IsNotExist(err) {
		t.Errorf("a pass that could not list must not write state: %v", err)
	}
}

func TestListOtherFailures(t *testing.T) {
	setupEnv(t)
	fake := NewFakeOsxphotos(t)
	c := Client{Bin: fake.Bin}

	fake.Fail(t, "query", "Error: no such album option\n", 2)
	_, err := c.List(context.Background(), album)
	var exit *exec.ExitError
	if err == nil || errors.Is(err, ErrNoAccess) || !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(err.Error(), "no such album option") {
		t.Fatalf("exit 2: %v", err)
	}

	fake.Fail(t, "query", "Error: could not open library\n", 0)
	fake.SetQueryStdout(t, "")
	if _, err := c.List(context.Background(), album); err == nil || !strings.Contains(err.Error(), "could not open library") {
		t.Fatalf("exit 0 with an error on stderr and no output: %v", err)
	}

	fake.Fail(t, "query", "", 0)
	fake.SetQueryStdout(t, "Loading database\n")
	if _, err := c.List(context.Background(), album); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("non-JSON stdout: %v", err)
	}
}

// osxphotos prints nothing (exit 0) when the album has no movies.
func TestListEmptyStdoutIsNoAssets(t *testing.T) {
	setupEnv(t)
	fake := NewFakeOsxphotos(t)
	c := Client{Bin: fake.Bin}
	for _, out := range []string{"", "\n  \n", "[]"} {
		fake.SetQueryStdout(t, out)
		assets, err := c.List(context.Background(), album)
		if err != nil || len(assets) != 0 {
			t.Errorf("stdout %q: %v, %v", out, assets, err)
		}
	}
	fake.SetAssets(t, FakeMovie("U1", "IMG_1.MOV", "", 7))
	assets, err := c.List(context.Background(), album)
	if err != nil || len(assets) != 1 {
		t.Fatalf("one movie: %v, %v", assets, err)
	}
	a := assets[0]
	if a.UUID != "U1" || a.Path != "" || !a.IsMissing || !a.IsMovie || a.InCloud == nil || !*a.InCloud || a.OriginalFilesize != 7 || a.Ready() {
		t.Errorf("decoded asset = %+v", a)
	}
}

func TestFindBin(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit-osxphotos")
	fromEnv := filepath.Join(dir, "env-osxphotos")
	for _, p := range []string{explicit, fromEnv} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv(BinEnv, fromEnv)

	if got, err := FindBin(explicit); err != nil || got != explicit {
		t.Errorf("explicit: %q, %v", got, err)
	}
	if got, err := FindBin(""); err != nil || got != fromEnv {
		t.Errorf("env: %q, %v", got, err)
	}
	// A configured path that is wrong is an error naming it, even though
	// $LONGFORM_OSXPHOTOS would resolve.
	missing := filepath.Join(dir, "typo-osxphotos")
	if got, err := FindBin(missing); !errors.Is(err, ErrNoBin) || got != "" || !strings.Contains(err.Error(), missing) {
		t.Errorf("missing explicit: %q, %v", got, err)
	}
	if got, err := FindBin(dir); !errors.Is(err, ErrNoBin) || got != "" || !strings.Contains(err.Error(), dir+" is a directory") {
		t.Errorf("explicit directory: %q, %v", got, err)
	}

	pathDir := t.TempDir()
	onPath := filepath.Join(pathDir, "osxphotos")
	if err := os.WriteFile(onPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BinEnv, "")
	t.Setenv("PATH", pathDir)
	if got, err := FindBin(""); err != nil || got != onPath {
		t.Errorf("PATH: %q, %v", got, err)
	}

	t.Setenv("PATH", t.TempDir())
	for _, p := range []string{"/opt/homebrew/bin/osxphotos", "/usr/local/bin/osxphotos"} {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("%s exists on this machine; cannot test ErrNoBin", p)
		}
	}
	if _, err := FindBin(""); !errors.Is(err, ErrNoBin) {
		t.Errorf("nothing anywhere: %v", err)
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 999: "999 B", 1000: "1.0 kB", 1500: "1.5 kB", 1_500_000: "1.5 MB",
		3_400_000_000: "3.4 GB", 2_000_000_000_000: "2.0 TB",
	} {
		if got := HumanSize(n); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"IMG_0001.MOV": "IMG_0001.MOV", "My Clip.mov": "My Clip.mov",
		"../../etc/passwd": "passwd", `a\b.mov`: "b.mov",
		"": "recording.mov", ".": "recording.mov", "..": "recording.mov", "/": "recording.mov",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCloneProducesIdenticalFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mov")
	data := bytes.Repeat([]byte("0123456789abcdef"), 64<<10) // 1 MiB
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "nested", "deeper", "dst.mov")
	if err := Clone(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dst); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("clone differs: %d bytes, %v", len(b), err)
	}
	// An existing dst (a clone from an interrupted pass) is replaced.
	if err := os.WriteFile(src, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Clone(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "second" {
		t.Errorf("dst not replaced: %q", b)
	}
	if err := Clone(filepath.Join(dir, "missing.mov"), filepath.Join(dir, "out.mov")); err == nil {
		t.Error("cloning a missing file must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "out.mov")); !os.IsNotExist(err) {
		t.Errorf("failed clone left a file: %v", err)
	}
}

// copyFile is the fallback across volumes (clonefile returns EXDEV there).
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mov")
	data := bytes.Repeat([]byte{0, 1, 2, 3, 255}, 200_000)
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.mov")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dst); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("copy differs: %d bytes, %v", len(b), err)
	}
	if _, err := os.Stat(dst + ".partial"); !os.IsNotExist(err) {
		t.Errorf("partial left behind: %v", err)
	}
	if err := copyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "x")); err == nil {
		t.Error("copy of a missing file must fail")
	}
	if err := copyFile(src, filepath.Join(dir, "no-such-dir", "x")); err == nil {
		t.Error("copy into a missing dir must fail")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".partial") {
			t.Errorf("partial left behind: %s", e.Name())
		}
	}
}
