package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/photos"
)

// PhotosPass feeds the recordings tagged in the profile's Photos album: the
// ready originals are cloned locally and staged together exactly like an
// inbox batch (same hashing, probe, stitch grouping, index and job layout),
// the rest keep waiting for iCloud Photos. Each job records the Photos assets
// it was made from, in source order, as job.extra.photos. popts.Feed is set
// here; the other fields are optional.
func PhotosPass(ctx context.Context, p *model.Profile, opts Options, popts photos.Options) (photos.Report, error) {
	if popts.Logf == nil {
		popts.Logf = opts.Logf
	}
	if popts.Logf == nil {
		popts.Logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}
	logf := popts.Logf
	popts.Feed = func(files []photos.FeedFile) (map[string]photos.FeedResult, error) {
		o := opts
		o.Files = make([]string, len(files))
		for i, f := range files {
			o.Files[i] = f.Path
		}
		o.StableSeconds = 0
		o.Once = true
		// Stage says why it skipped a file only in its log; keep the lines.
		var said []string
		stageLogf := o.Logf
		if stageLogf == nil {
			stageLogf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
		}
		o.Logf = func(format string, args ...any) {
			said = append(said, fmt.Sprintf(format, args...))
			stageLogf(format, args...)
		}
		jobs, err := Stage(p, o)
		if err != nil {
			return nil, err
		}
		results := map[string]photos.FeedResult{}
		for _, j := range jobs {
			var assets []any
			for _, s := range j.Sources {
				for _, f := range files {
					if samePath(s.Original, f.Path) {
						results[f.Path] = photos.FeedResult{JobID: j.ID}
						assets = append(assets, f.Extra)
					}
				}
			}
			if len(assets) == 0 {
				continue
			}
			if j.Extra == nil {
				j.Extra = map[string]any{}
			}
			j.Extra["photos"] = assets
			if err := j.Save(); err != nil {
				// The job is staged; only its provenance note is missing.
				logf("photos %s: job %s: record photos assets: %v", p.Name, j.ID, err)
			}
		}
		var ix *model.Index
		for _, f := range files {
			if _, ok := results[f.Path]; ok {
				continue
			}
			results[f.Path] = photos.FeedResult{Err: noJobReason(p, f.Path, said, &ix)}
		}
		return results, nil
	}
	return photos.Pass(ctx, p, popts)
}

// noJobReason explains a feed file stage made no job of: "" when its bytes
// are already indexed (a true duplicate), otherwise what stage logged about
// it, a failed stage group, or a generic note.
func noJobReason(p *model.Profile, path string, said []string, ix **model.Index) string {
	if _, err := os.Stat(path); err == nil {
		h, err := sha256File(path)
		if err != nil {
			return "hash: " + err.Error()
		}
		if *ix == nil {
			if *ix, err = model.LoadIndex(); err != nil {
				return "index: " + err.Error()
			}
		}
		if _, dup := (*ix).Has(h); dup {
			return ""
		}
	}
	abs, _ := filepath.Abs(path)
	for i := len(said) - 1; i >= 0; i-- {
		if strings.Contains(said[i], path) || (abs != "" && strings.Contains(said[i], abs)) {
			return said[i]
		}
	}
	// stageGroup failures name the job, not the file ("stage <p>: job <id>:
	// stitch: ..."); the success line is "stage <p>: job <id> staged (...)".
	var group []string
	prefix := "stage " + p.Name + ": "
	for _, l := range said {
		rest, ok := strings.CutPrefix(l, prefix)
		if ok && (strings.HasPrefix(rest, "job ") || strings.HasPrefix(rest, "group ")) && !strings.Contains(rest, " staged (") {
			group = append(group, rest)
		}
	}
	if len(group) > 0 {
		return "stage group failed: " + strings.Join(group, "; ")
	}
	return "stage created no job (see the watch log)"
}

// samePath compares a job source's original (stage makes it absolute) with a
// path the feed handed over.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && aa == bb
}

// Driver runs one job stage at a time by exec'ing the longform binary, so
// every stage is the same code path whether launchd or a human runs it.
type Driver struct {
	// Self is the executable to run (SelfExecutable()).
	Self string
	// Logf receives watcher progress; default stderr.
	Logf func(format string, args ...any)
}

func (d *Driver) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// step is one hop the watcher drives: run cmd when the job is in from, and
// expect it to have reached to afterwards.
type step struct {
	from model.State
	cmd  string
	to   model.State
}

var steps = []step{
	{model.StateStaged, "prepare", model.StatePrepared},
	{model.StatePrepared, "upload", model.StateUploaded},
	{model.StateUploaded, "wait", model.StateClipped},
}

// SessionLaunchedKey is the job.extra key that marks launch-session as done.
const SessionLaunchedKey = "session_launched_at"

// Pass stages every profile's inbox (or one profile when profile is not
// "all"/""), then drives the jobs of those profiles. Per-job failures are
// logged, never returned; the error is for conditions that stop the pass.
func (d *Driver) Pass(profile string, opts Options) error {
	var names []string
	if profile == "" || profile == "all" {
		var err error
		if names, err = model.ListProfiles(); err != nil {
			return fmt.Errorf("list profiles: %w", err)
		}
	} else {
		names = []string{profile}
	}
	sort.Strings(names)
	known := map[string]bool{}
	for _, name := range names {
		p, err := model.LoadProfile(name)
		if err != nil {
			d.logf("watch: profile %s: %v", name, err)
			continue
		}
		known[p.Name] = true
		if _, err := Stage(p, opts); err != nil {
			d.logf("watch: stage %s: %v", p.Name, err)
		}
		if p.Photos.Enabled {
			rep, err := PhotosPass(context.Background(), p, opts, photos.Options{})
			if err != nil {
				d.logf("watch: photos %s: %v", p.Name, err)
			} else if rep.Seen > 0 {
				d.logf("watch: photos %s: album %q: %d seen, %d waiting, %d deferred, %d fed now, %d done before, %d failed", p.Name, p.Photos.Album, rep.Seen, rep.Waiting, rep.Deferred, rep.Fed, rep.Done, rep.Failed)
			}
		}
	}
	jobs, err := model.ListJobs()
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}
	var mine []*model.Job
	for _, j := range jobs {
		if known[j.Profile] {
			mine = append(mine, j)
		}
	}
	d.Drive(mine)
	return nil
}

// Drive advances every job that is not yet clipped through prepare, upload
// and wait, stopping a job at the first step that fails or does not advance,
// and launches the editorial session once for every clipped job. Past
// approval it runs monitor on a scheduled job, and manifest expire then close
// on a monitored one; an approved job (the approval phase never sealed) is
// only logged. Jobs are processed oldest first; a profile with a job between
// uploaded and clipped does not start another upload. Failures are logged,
// never returned.
func (d *Driver) Drive(jobs []*model.Job) {
	sort.SliceStable(jobs, func(i, k int) bool {
		if !jobs[i].CreatedAt.Equal(jobs[k].CreatedAt) {
			return jobs[i].CreatedAt.Before(jobs[k].CreatedAt)
		}
		return jobs[i].ID < jobs[k].ID
	})
	for i := range jobs {
		j := jobs[i]
		if j.State == model.StateFailed || j.State == model.StateClosed {
			continue
		}
		if !j.AtLeast(model.StateClipped) {
			for _, s := range steps {
				if j.AtLeast(s.to) {
					continue
				}
				if j.State != s.from {
					d.logf("watch: job %s is %s; nothing to run", j.ID, j.State)
					break
				}
				if s.cmd == "upload" && d.inFlight(jobs, i) {
					d.logf("watch: job %s waits; another %s job is in flight", j.ID, j.Profile)
					break
				}
				code, err := d.run(j, s.cmd)
				if nj, lerr := model.LoadJob(j.Dir()); lerr == nil {
					jobs[i], j = nj, nj
				}
				if err != nil || code != 0 {
					d.logf("watch: job %s: %s exited %d (%v); see %s", j.ID, s.cmd, code, err, j.Path(filepath.Join("logs", s.cmd+".log")))
					break
				}
				if !j.AtLeast(s.to) {
					d.logf("watch: job %s: %s exited 0 but state is still %s; stopping", j.ID, s.cmd, j.State)
					break
				}
				d.logf("watch: job %s: %s -> %s", j.ID, s.cmd, j.State)
			}
		}
		if j.AtLeast(model.StateClipped) && j.State != model.StateFailed && !SessionLaunched(j) {
			code, err := d.run(j, "launch-session")
			if nj, lerr := model.LoadJob(j.Dir()); lerr == nil {
				jobs[i], j = nj, nj
			}
			if err != nil || code != 0 {
				d.logf("watch: job %s: launch-session exited %d (%v)", j.ID, code, err)
			} else {
				d.logf("watch: job %s: session launched", j.ID)
			}
		}
		jobs[i] = d.afterApproval(j)
	}
}

// afterApproval drives the states past approval: a scheduled job is
// monitored, then a monitored job has its backlog expired and is closed once
// the window is over (close exiting 0 without advancing is the normal case
// for weeks). An approved job means the approval phase stopped before
// `manifest seal`; the watcher cannot finish it and says so once per pass.
func (d *Driver) afterApproval(j *model.Job) *model.Job {
	switch j.State {
	case model.StateApproved:
		d.logf("watch: job %s is approved but not sealed; re-run the approval phase, resuming at longform manifest seal --job %s (it lists the cells still to post)", j.ID, j.Dir())
		return j
	case model.StateScheduled:
		j = d.hop(j, "monitor", []string{"monitor", j.Dir()}, model.StateMonitored)
	}
	if j.State != model.StateMonitored {
		return j
	}
	code, err := d.runArgs(j, "expire", "manifest", "expire", "--job", j.Dir())
	if err != nil || code != 0 {
		d.logf("watch: job %s: manifest expire exited %d (%v); see %s", j.ID, code, err, j.Path(filepath.Join("logs", "expire.log")))
		return j
	}
	// Staying monitored is the normal answer for weeks; close.log says why,
	// and the watcher's own log only gets the transition.
	return d.hop(j, "close", []string{"close", j.Dir()}, model.StateClosed)
}

// hop runs one post-approval command, reloads the job and logs the outcome.
// A command that exits 0 without reaching to is not an error here (close
// waits out the backlog that way).
func (d *Driver) hop(j *model.Job, logName string, args []string, to model.State) *model.Job {
	code, err := d.runArgs(j, logName, args...)
	if nj, lerr := model.LoadJob(j.Dir()); lerr == nil {
		j = nj
	}
	switch {
	case err != nil || code != 0:
		d.logf("watch: job %s: %s exited %d (%v); see %s", j.ID, logName, code, err, j.Path(filepath.Join("logs", logName+".log")))
	case j.State == to:
		d.logf("watch: job %s: %s -> %s", j.ID, logName, j.State)
	}
	return j
}

// SessionLaunched reports whether launch-session already ran for the job.
func SessionLaunched(j *model.Job) bool {
	v, _ := j.Extra[SessionLaunchedKey].(string)
	return v != ""
}

// inFlight reports whether another job of the same profile sits between
// uploaded and clipped (OpusClip credits spent, render not yet collected).
func (d *Driver) inFlight(jobs []*model.Job, self int) bool {
	for k, o := range jobs {
		if k == self || o.Profile != jobs[self].Profile {
			continue
		}
		if o.State == model.StateUploaded {
			return true
		}
	}
	return false
}

// run execs `<self> <cmd> <jobdir>` with stdout and stderr appended to
// <jobdir>/logs/<cmd>.log and returns the exit code.
func (d *Driver) run(j *model.Job, cmd string) (int, error) {
	return d.runArgs(j, cmd, cmd, j.Dir())
}

// runArgs execs `<self> <args...>` with stdout and stderr appended to
// <jobdir>/logs/<logName>.log and returns the exit code.
func (d *Driver) runArgs(j *model.Job, logName string, args ...string) (int, error) {
	logPath := j.Path(filepath.Join("logs", logName+".log"))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return -1, err
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return -1, err
	}
	defer f.Close()
	fmt.Fprintf(f, "=== %s longform %s\n", time.Now().UTC().Format(time.RFC3339), strings.Join(args, " "))
	c := exec.Command(d.Self, args...)
	c.Stdout, c.Stderr = f, f
	c.Env = os.Environ()
	err = c.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
		err = nil
	default:
		code = -1
	}
	fmt.Fprintf(f, "=== exit %d\n", code)
	return code, err
}
