package ingest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

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
// and launches the editorial session once for every clipped job. Jobs are
// processed oldest first; a profile with a job between uploaded and clipped
// does not start another upload.
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
	}
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
	logPath := j.Path(filepath.Join("logs", cmd+".log"))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return -1, err
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return -1, err
	}
	defer f.Close()
	fmt.Fprintf(f, "=== %s longform %s %s\n", time.Now().UTC().Format(time.RFC3339), cmd, j.Dir())
	c := exec.Command(d.Self, cmd, j.Dir())
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
