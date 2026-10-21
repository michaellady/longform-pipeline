package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/notify"
)

// StitchedName is the job-relative path of the concatenated file for a
// stitch job; it is also recorded in job.extra.stitched_path.
const StitchedName = "stitched.mp4"

var videoExts = map[string]bool{".mov": true, ".mp4": true, ".m4v": true}

// IsVideo reports whether a file name carries a video extension the inbox
// accepts (.mov .mp4 .m4v, any case).
func IsVideo(name string) bool { return videoExts[strings.ToLower(filepath.Ext(name))] }

// Options tunes one staging pass.
type Options struct {
	// StableSeconds is how long size and mtime must hold before a file is
	// taken. 0 takes a file on first sight (tests).
	StableSeconds int
	// PollInterval is the stat cadence while waiting; default 5 s, capped to
	// the stability window.
	PollInterval time.Duration
	// Once skips a file that changes during the window instead of waiting
	// for it to settle (launchd WatchPaths mode).
	Once bool
	// Files, when set, replaces the inbox scan with an explicit list.
	Files []string
	// Now stamps jobs; default time.Now.
	Now func() time.Time
	// Logf receives progress lines; default stderr.
	Logf func(format string, args ...any)
	// Notify receives job events; default notify.Send (errors ignored).
	Notify func(p *model.Profile, j *model.Job, event, message string)
}

func (o *Options) defaults() {
	if o.StableSeconds < 0 {
		o.StableSeconds = 0
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 5 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}
	if o.Notify == nil {
		o.Notify = func(p *model.Profile, j *model.Job, event, message string) { _ = notify.Send(p, j, event, message) }
	}
}

type fileObs struct {
	path   string
	size   int64
	mtime  time.Time
	exists bool
}

func (a fileObs) same(b fileObs) bool {
	return a.exists == b.exists && a.size == b.size && a.mtime.Equal(b.mtime)
}

func statObs(path string) fileObs {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return fileObs{path: path}
	}
	return fileObs{path: path, size: st.Size(), mtime: st.ModTime(), exists: true}
}

type candidate struct {
	path       string
	size       int64
	mtime      time.Time
	hash       string
	probe      *model.Probe
	hasAudio   bool
	recordedAt time.Time
}

// Stage runs one inbox pass for a profile and returns the jobs it created.
// Files that are still arriving are left for the next pass; duplicates by
// hash are logged and left where they are.
func Stage(p *model.Profile, opts Options) ([]*model.Job, error) {
	opts.defaults()
	paths, err := listCandidates(p, opts)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	stable, dropped := waitStable(paths, opts)
	for _, d := range dropped {
		opts.Logf("stage %s: %s still changing or not materialised; skipping this pass", p.Name, d)
	}
	if p.Stitch.Enabled && len(dropped) > 0 {
		// A BJJ batch arrives as several files; grouping them before the
		// last one lands would split the session into two jobs.
		opts.Logf("stage %s: %d file(s) still arriving; deferring stitch grouping until the batch settles", p.Name, len(dropped))
		return nil, nil
	}
	if len(stable) == 0 {
		return nil, nil
	}
	ix, err := model.LoadIndex()
	if err != nil {
		return nil, err
	}
	var cands []*candidate
	seen := map[string]bool{}
	for _, f := range stable {
		h, err := sha256File(f.path)
		if err != nil {
			opts.Logf("stage %s: hash %s: %v", p.Name, f.path, err)
			continue
		}
		if id, dup := ix.Has(h); dup {
			opts.Logf("stage %s: %s duplicates job %s; skipping", p.Name, f.path, id)
			continue
		}
		if seen[h] {
			opts.Logf("stage %s: %s duplicates another file in this pass; skipping", p.Name, f.path)
			continue
		}
		seen[h] = true
		c := &candidate{path: f.path, size: f.size, mtime: f.mtime, hash: h, recordedAt: f.mtime}
		pr, err := Probe(f.path)
		switch {
		case errors.Is(err, ErrNoFFprobe):
			opts.Logf("stage %s: ffprobe not found; staging %s without a probe", p.Name, f.path)
		case err != nil:
			opts.Logf("stage %s: %s is not a readable video (%v); skipping", p.Name, f.path, err)
			continue
		default:
			probe := pr.Probe
			c.probe = &probe
			c.hasAudio = pr.HasAudio
			if !pr.CreationTime.IsZero() {
				c.recordedAt = pr.CreationTime
			}
		}
		cands = append(cands, c)
	}
	var jobs []*model.Job
	for _, g := range group(p, cands) {
		j, err := stageGroup(p, g, ix, opts)
		if err != nil {
			opts.Logf("stage %s: %v", p.Name, err)
			continue
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// listCandidates returns the absolute paths to consider this pass and asks
// iCloud to download any placeholder.
func listCandidates(p *model.Profile, opts Options) ([]string, error) {
	if len(opts.Files) > 0 {
		var out []string
		for _, f := range opts.Files {
			abs, err := filepath.Abs(f)
			if err != nil {
				return nil, err
			}
			if !IsVideo(abs) {
				opts.Logf("stage %s: %s is not a video file; skipping", p.Name, abs)
				continue
			}
			requestDownload(abs, false)
			out = append(out, abs)
		}
		return out, nil
	}
	if p.Inbox == "" {
		return nil, errors.New("profile has no inbox")
	}
	if err := os.MkdirAll(p.Inbox, 0o755); err != nil {
		return nil, fmt.Errorf("inbox %s: %w", p.Inbox, err)
	}
	entries, err := os.ReadDir(p.Inbox)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			// iCloud shows an evicted file as .<name>.icloud until downloaded.
			if real, ok := strings.CutSuffix(strings.TrimPrefix(name, "."), ".icloud"); ok && IsVideo(real) {
				full := filepath.Join(p.Inbox, real)
				requestDownload(full, true)
				out = append(out, full)
			}
			continue
		}
		if !e.Type().IsRegular() || !IsVideo(name) {
			continue
		}
		full := filepath.Join(p.Inbox, name)
		requestDownload(full, false)
		out = append(out, full)
	}
	sort.Strings(out)
	return out, nil
}

// requestDownload asks iCloud to materialise a file. brctl returns before
// the download finishes; waitStable then watches for the file to appear.
// Missing brctl (Linux CI, tests) is a no-op.
func requestDownload(path string, placeholder bool) {
	if !placeholder && !strings.Contains(path, "/Mobile Documents/") {
		return
	}
	brctl := LookPath("brctl")
	if brctl == "" {
		return
	}
	cmd := exec.Command(brctl, "download", path)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	_ = cmd.Run()
}

// waitStable returns the files whose size and mtime held for the window and
// the paths it gave up on (still changing in Once mode, never materialised,
// or past the loop-mode cap).
func waitStable(paths []string, opts Options) (stable []fileObs, dropped []string) {
	window := time.Duration(opts.StableSeconds) * time.Second
	poll := opts.PollInterval
	if window > 0 && poll > window {
		poll = window
	}
	maxWait := max(window, time.Hour)
	type track struct {
		obs   fileObs
		since time.Time
	}
	start := time.Now()
	tracks := make(map[string]*track, len(paths))
	for _, p := range paths {
		tracks[p] = &track{obs: statObs(p), since: start}
	}
	for len(tracks) > 0 {
		now := time.Now()
		for p, t := range tracks {
			cur := statObs(p)
			switch {
			case !cur.same(t.obs):
				appeared := !t.obs.exists && cur.exists
				if opts.Once && !appeared {
					dropped = append(dropped, p)
					delete(tracks, p)
					continue
				}
				t.obs, t.since = cur, now
			case !cur.exists:
				if now.Sub(t.since) >= window {
					dropped = append(dropped, p)
					delete(tracks, p)
				}
			case now.Sub(t.since) >= window:
				stable = append(stable, cur)
				delete(tracks, p)
			}
		}
		if len(tracks) == 0 {
			break
		}
		if time.Since(start) > maxWait {
			for p := range tracks {
				dropped = append(dropped, p)
			}
			break
		}
		time.Sleep(poll)
	}
	sort.Slice(stable, func(i, j int) bool { return stable[i].path < stable[j].path })
	sort.Strings(dropped)
	return stable, dropped
}

// group orders candidates by recording time and, for a stitch profile,
// chains files whose recording times fall within the session window.
func group(p *model.Profile, cands []*candidate) [][]*candidate {
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if !cands[i].recordedAt.Equal(cands[j].recordedAt) {
			return cands[i].recordedAt.Before(cands[j].recordedAt)
		}
		return cands[i].path < cands[j].path
	})
	if !p.Stitch.Enabled {
		out := make([][]*candidate, 0, len(cands))
		for _, c := range cands {
			out = append(out, []*candidate{c})
		}
		return out
	}
	window := time.Duration(p.Stitch.SessionWindowMin) * time.Minute
	var out [][]*candidate
	cur := []*candidate{cands[0]}
	for _, c := range cands[1:] {
		if c.recordedAt.Sub(cur[len(cur)-1].recordedAt) <= window {
			cur = append(cur, c)
			continue
		}
		out = append(out, cur)
		cur = []*candidate{c}
	}
	return append(out, cur)
}

// ContentHash is sha256 over the sorted source hashes joined by newline; it
// identifies a job regardless of file names or arrival order.
func ContentHash(hashes []string) string {
	s := slices.Clone(hashes)
	sort.Strings(s)
	sum := sha256.Sum256([]byte(strings.Join(s, "\n")))
	return hex.EncodeToString(sum[:])
}

func stageGroup(p *model.Profile, g []*candidate, ix *model.Index, opts Options) (*model.Job, error) {
	hashes := make([]string, len(g))
	for i, c := range g {
		hashes[i] = c.hash
	}
	contentHash := ContentHash(hashes)
	if id, ok := ix.Has(contentHash); ok {
		return nil, fmt.Errorf("group %s duplicates job %s", contentHash[:8], id)
	}
	sources := make([]model.Source, len(g))
	for i, c := range g {
		sources[i] = model.Source{
			ID:         c.hash[:12],
			Path:       filepath.Join("sources", fmt.Sprintf("%02d-%s", i+1, filepath.Base(c.path))),
			Original:   c.path,
			SHA256:     c.hash,
			SizeBytes:  c.size,
			RecordedAt: c.recordedAt.UTC(),
			Probe:      c.probe,
		}
	}
	job, err := model.NewJob(p.Name, contentHash, sources, opts.Now())
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*model.Job, error) {
		_ = job.Advance(model.StateFailed, "stage", err.Error())
		opts.Notify(p, job, "failed", err.Error())
		return nil, fmt.Errorf("job %s: %w", job.ID, err)
	}
	for _, s := range sources {
		if err := moveFile(s.Original, job.Path(s.Path)); err != nil {
			return fail(fmt.Errorf("move %s: %w", s.Original, err))
		}
	}
	note := fmt.Sprintf("%d source(s)", len(sources))
	if len(sources) > 1 {
		inputs := make([]string, len(sources))
		for i, s := range sources {
			inputs[i] = job.Path(s.Path)
		}
		res, err := Stitch(inputs, job.Path(StitchedName), opts.Logf)
		if err != nil {
			return fail(fmt.Errorf("stitch: %w", err))
		}
		job.Stitched = true
		if job.Extra == nil {
			job.Extra = map[string]any{}
		}
		job.Extra["stitched_path"] = StitchedName
		job.Extra["stitched_mode"] = res.Mode
		job.Extra["stitched_duration_ms"] = res.DurationMs
		if err := job.Save(); err != nil {
			return fail(err)
		}
		note += ", stitched (" + res.Mode + ")"
	}
	for _, h := range hashes {
		if err := ix.Add(h, job.ID); err != nil {
			return fail(fmt.Errorf("index: %w", err))
		}
	}
	if err := ix.Add(contentHash, job.ID); err != nil {
		return fail(fmt.Errorf("index: %w", err))
	}
	if err := job.Advance(model.StateStaged, "stage", note); err != nil {
		return fail(err)
	}
	opts.Logf("stage %s: job %s staged (%s)", p.Name, job.ID, note)
	opts.Notify(p, job, "staged", note)
	return job, nil
}

// InputPath is the file the prepare stage should read: the stitched
// concatenation for a stitch job, otherwise the first (only) source.
func InputPath(j *model.Job) string {
	if j.Stitched {
		if v, ok := j.Extra["stitched_path"].(string); ok && v != "" {
			return j.Path(v)
		}
		return j.Path(StitchedName)
	}
	if len(j.Sources) > 0 {
		return j.Path(j.Sources[0].Path)
	}
	return ""
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// moveFile renames src to dst, falling back to copy+remove across volumes.
// The original is removed only after the copy's size matches.
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	if n != st.Size() {
		os.Remove(dst)
		return fmt.Errorf("copied %d of %d bytes", n, st.Size())
	}
	return os.Remove(src)
}
