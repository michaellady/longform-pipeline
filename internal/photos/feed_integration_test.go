package photos_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/ingest"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/photos"
)

// These run the album feed end to end through the real stage: osxphotos is
// the stub from stub_test.go, everything else (clonefile, hashing, ffprobe,
// stitch, index, job layout) is real.

const albumName = "longform-evc"

func setupEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LONGFORM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("LONGFORM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("LONGFORM_LOG_DIR", filepath.Join(root, "logs"))
	t.Setenv("LONGFORM_SLACK_WEBHOOK", "")
}

// libraryOriginal renders a 2 s test recording into a stand-in Photos library.
func libraryOriginal(t *testing.T, name, creation string) string {
	t.Helper()
	ffmpeg := ingest.LookPath("ffmpeg")
	if ffmpeg == "" || ingest.LookPath("ffprobe") == "" {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	out := filepath.Join(t.TempDir(), "Photos Library.photoslibrary", "originals", "F", name)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac",
		"-metadata", "creation_time=" + creation, "-f", "mov", out}
	if b, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture: %v\n%s", err, b)
	}
	return out
}

func size(t *testing.T, p string) int64 {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func sum(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// notices collects the feed's notifications for the current test.
var notices []string

func photosPass(t *testing.T, p *model.Profile, fake *photos.FakeOsxphotos) photos.Report {
	t.Helper()
	rep, err := ingest.PhotosPass(context.Background(), p,
		ingest.Options{PollInterval: 50 * time.Millisecond, Logf: t.Logf, Notify: func(*model.Profile, *model.Job, string, string) {}},
		photos.Options{Client: photos.Client{Bin: fake.Bin}, Logf: t.Logf,
			Notify: func(_ *model.Profile, event, message string) { notices = append(notices, event+": "+message) }})
	if err != nil {
		t.Fatalf("PhotosPass: %v", err)
	}
	return rep
}

func feedRecord(t *testing.T, profile, uuid string) *photos.Record {
	t.Helper()
	st, err := photos.LoadState(profile)
	if err != nil {
		t.Fatal(err)
	}
	r := st.Assets[uuid]
	if r == nil {
		t.Fatalf("no feed record for %s", uuid)
	}
	return r
}

func provenance(uuid, path, name string) map[string]any {
	return map[string]any{"uuid": uuid, "album": albumName, "original_path": path,
		"original_filename": name, "date": "2026-10-20T09:15:00-07:00"}
}

func jobCount(t *testing.T) int {
	t.Helper()
	jobs, err := model.ListJobs()
	if err != nil {
		t.Fatal(err)
	}
	return len(jobs)
}

// Non-stitch profile: each ready recording becomes its own staged job, the
// clone (not the library original) is moved into sources/, and the job
// records its Photos asset in extra.photos. Later passes do not re-stage, and
// a byte-identical recording is settled as a duplicate.
func TestPhotosPassStagesEachRecordingAsItsOwnJob(t *testing.T) {
	setupEnv(t)
	fake := photos.NewFakeOsxphotos(t)
	lib1 := libraryOriginal(t, "IMG_0101.MOV", "2026-10-20T16:00:00Z")
	lib2 := libraryOriginal(t, "IMG_0102.MOV", "2026-10-20T16:10:00Z")
	fake.SetAssets(t,
		photos.FakeMovie("UUID-1", "IMG_0101.MOV", lib1, size(t, lib1)),
		photos.FakeMovie("UUID-2", "IMG_0102.MOV", lib2, size(t, lib2)))
	p := &model.Profile{Name: "evc", Orientation: "landscape", Photos: model.PhotosConfig{Enabled: true, Album: albumName}}

	if rep := photosPass(t, p, fake); rep != (photos.Report{Seen: 2, Fed: 2}) {
		t.Fatalf("report = %+v", rep)
	}
	if n := jobCount(t); n != 2 {
		t.Fatalf("jobs = %d, want 2", n)
	}
	for _, c := range []struct{ uuid, lib, name string }{{"UUID-1", lib1, "IMG_0101.MOV"}, {"UUID-2", lib2, "IMG_0102.MOV"}} {
		rec := feedRecord(t, "evc", c.uuid)
		if rec.Status != photos.StatusFed || rec.JobID == "" {
			t.Fatalf("%s record = %+v", c.uuid, *rec)
		}
		j, err := model.LoadJob(rec.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if j.State != model.StateStaged || j.Stitched || len(j.Sources) != 1 {
			t.Fatalf("%s job state %s stitched %v sources %d", c.uuid, j.State, j.Stitched, len(j.Sources))
		}
		s := j.Sources[0]
		if s.Path != "sources/01-"+c.name || s.Original != rec.Incoming || s.Probe == nil || s.Probe.Codec != "h264" {
			t.Errorf("%s source = %+v (incoming %s)", c.uuid, s, rec.Incoming)
		}
		if got := sum(t, j.Path(s.Path)); got != sum(t, c.lib) || got != s.SHA256 {
			t.Errorf("%s staged bytes differ from the library original", c.uuid)
		}
		if _, err := os.Stat(rec.Incoming); !os.IsNotExist(err) {
			t.Errorf("%s clone should have moved into the job: %v", c.uuid, err)
		}
		if _, err := os.Stat(c.lib); err != nil {
			t.Errorf("%s library original must stay: %v", c.uuid, err)
		}
		want := []any{provenance(c.uuid, c.lib, c.name)}
		if !reflect.DeepEqual(j.Extra["photos"], want) {
			t.Errorf("%s extra.photos = %#v\nwant %#v", c.uuid, j.Extra["photos"], want)
		}
	}

	if rep := photosPass(t, p, fake); rep != (photos.Report{Seen: 2, Done: 2}) || jobCount(t) != 2 {
		t.Fatalf("second pass: %+v, %d jobs", rep, jobCount(t))
	}

	// Same bytes under a new asset (re-shared to the album): stage's hash
	// index makes it a duplicate, which the feed settles without a job.
	dupLib := filepath.Join(t.TempDir(), "IMG_0103.MOV")
	b, err := os.ReadFile(lib1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dupLib, b, 0o644); err != nil {
		t.Fatal(err)
	}
	fake.SetAssets(t,
		photos.FakeMovie("UUID-1", "IMG_0101.MOV", lib1, size(t, lib1)),
		photos.FakeMovie("UUID-2", "IMG_0102.MOV", lib2, size(t, lib2)),
		photos.FakeMovie("UUID-3", "IMG_0103.MOV", dupLib, int64(len(b))))
	if rep := photosPass(t, p, fake); rep != (photos.Report{Seen: 3, Fed: 1, Done: 2}) || jobCount(t) != 2 {
		t.Fatalf("duplicate pass: %+v, %d jobs", rep, jobCount(t))
	}
	rec := feedRecord(t, "evc", "UUID-3")
	if rec.Status != photos.StatusFed || rec.JobID != "" || !strings.Contains(rec.Note, "duplicate") {
		t.Errorf("duplicate record = %+v", *rec)
	}
	if _, err := os.Stat(filepath.Dir(rec.Incoming)); !os.IsNotExist(err) {
		t.Errorf("duplicate clone dir should be removed: %v", err)
	}
}

// A recording ffprobe rejects is not a duplicate: the feed fails it with
// stage's reason, keeps the clone for inspection and notifies once, while a
// good recording in the same batch is staged.
func TestPhotosPassFailsUnreadableVideo(t *testing.T) {
	setupEnv(t)
	notices = nil
	fake := photos.NewFakeOsxphotos(t)
	good := libraryOriginal(t, "IMG_0302.MOV", "2026-10-20T16:00:00Z")
	junk := filepath.Join(t.TempDir(), "IMG_0301.MOV")
	if err := os.WriteFile(junk, []byte("not a movie: truncated download"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.SetAssets(t,
		photos.FakeMovie("UUID-JUNK", "IMG_0301.MOV", junk, 31),
		photos.FakeMovie("UUID-GOOD", "IMG_0302.MOV", good, size(t, good)))
	p := &model.Profile{Name: "evc", Orientation: "landscape", Photos: model.PhotosConfig{Enabled: true, Album: albumName}}

	if rep := photosPass(t, p, fake); rep != (photos.Report{Seen: 2, Fed: 1, Failed: 1}) || jobCount(t) != 1 {
		t.Fatalf("report = %+v, %d jobs", rep, jobCount(t))
	}
	rec := feedRecord(t, "evc", "UUID-JUNK")
	if rec.Status != photos.StatusFailed || rec.JobID != "" || !strings.Contains(rec.Note, "is not a readable video") || !strings.Contains(rec.Note, "IMG_0301.MOV") {
		t.Errorf("junk record = %+v", *rec)
	}
	if b, err := os.ReadFile(rec.Incoming); err != nil || string(b) != "not a movie: truncated download" {
		t.Errorf("rejected clone must be kept: %q, %v", b, err)
	}
	if len(notices) != 1 || !strings.HasPrefix(notices[0], "failed: Could not stage IMG_0301.MOV from the Photos album: ") {
		t.Errorf("notices = %q", notices)
	}
	if r := feedRecord(t, "evc", "UUID-GOOD"); r.Status != photos.StatusFed || r.JobID == "" {
		t.Errorf("good record = %+v", *r)
	}

	if rep := photosPass(t, p, fake); rep != (photos.Report{Seen: 2, Done: 1, Failed: 1}) || len(notices) != 1 {
		t.Errorf("second pass: %+v, notices %q", rep, notices)
	}
}

// Stitch profile: a class recorded in two segments waits while one segment
// is still in iCloud, then both segments become ONE stitched job in
// recording order (not album order) with both assets in extra.photos.
func TestPhotosPassStitchesASession(t *testing.T) {
	setupEnv(t)
	fake := photos.NewFakeOsxphotos(t)
	part1 := libraryOriginal(t, "IMG_0201.MOV", "2026-10-20T16:00:00Z")
	part2 := libraryOriginal(t, "IMG_0202.MOV", "2026-10-20T16:20:00Z")
	p := &model.Profile{Name: "sandbox-bjj", Orientation: "landscape",
		Stitch: model.StitchConfig{Enabled: true, SessionWindowMin: 90},
		Photos: model.PhotosConfig{Enabled: true, Album: albumName}}

	// Album order is newest first; part 1 is still downloading.
	fake.SetAssets(t,
		photos.FakeMovie("UUID-P2", "IMG_0202.MOV", part2, size(t, part2)),
		photos.FakeMovie("UUID-P1", "IMG_0201.MOV", "", size(t, part1)))
	if rep := photosPass(t, p, fake); rep != (photos.Report{Seen: 2, Waiting: 1, Deferred: 1}) || jobCount(t) != 0 {
		t.Fatalf("partial batch: %+v, %d jobs", rep, jobCount(t))
	}

	fake.SetAssets(t,
		photos.FakeMovie("UUID-P2", "IMG_0202.MOV", part2, size(t, part2)),
		photos.FakeMovie("UUID-P1", "IMG_0201.MOV", part1, size(t, part1)))
	if rep := photosPass(t, p, fake); rep != (photos.Report{Seen: 2, Fed: 2}) {
		t.Fatalf("complete batch: %+v", rep)
	}
	jobs, err := model.ListJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want one stitched job", len(jobs))
	}
	j := jobs[0]
	if j.State != model.StateStaged || !j.Stitched || len(j.Sources) != 2 {
		t.Fatalf("job state %s stitched %v sources %d", j.State, j.Stitched, len(j.Sources))
	}
	if j.Sources[0].Path != "sources/01-IMG_0201.MOV" || j.Sources[1].Path != "sources/02-IMG_0202.MOV" {
		t.Errorf("sources not in recording order: %s, %s", j.Sources[0].Path, j.Sources[1].Path)
	}
	if _, err := os.Stat(j.Path(ingest.StitchedName)); err != nil {
		t.Errorf("stitched file: %v", err)
	}
	want := []any{provenance("UUID-P1", part1, "IMG_0201.MOV"), provenance("UUID-P2", part2, "IMG_0202.MOV")}
	if !reflect.DeepEqual(j.Extra["photos"], want) {
		t.Errorf("extra.photos = %#v\nwant %#v", j.Extra["photos"], want)
	}
	if j.Extra["stitched_path"] != ingest.StitchedName {
		t.Errorf("stage's own extra was lost: %v", j.Extra)
	}
	for _, u := range []string{"UUID-P1", "UUID-P2"} {
		if rec := feedRecord(t, "sandbox-bjj", u); rec.JobID != j.ID || rec.Status != photos.StatusFed {
			t.Errorf("%s record = %+v", u, *rec)
		}
	}
}
