package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/lifecycle"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/notify"
	"github.com/michaellady/longform-pipeline/internal/opusclip"
)

func init() {
	Register(Command{Name: "monitor", Short: "Read every scheduleId back from OpusClip and record it in manifest.json (scheduled -> monitored)", Run: runMonitor})
	Register(Command{Name: "close", Short: "Close a monitored job once its backlog window is over and nothing is held (monitored -> closed)", Run: runClose})
}

const (
	sealUsage    = "usage: longform manifest seal --job <jobdir> [--allow-missing] [--now RFC3339]"
	monitorUsage = "usage: longform monitor [--now RFC3339] (--job <jobdir> | <jobdir>)"
	closeUsage   = "usage: longform close [--now RFC3339] (--job <jobdir> | <jobdir>)"
)

// lifecycleFlags parses --job or one positional job dir plus --now, the
// shared shape of seal, monitor and close (the watcher passes the job dir
// positionally like every other job-stage command).
func lifecycleFlags(name, usage string, args []string, extra func(*flag.FlagSet)) (*model.Job, time.Time, int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	jobFlag := fs.String("job", "", "job directory")
	nowFlag := fs.String("now", "", "override the clock (RFC3339)")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, usage)
		return nil, time.Time{}, 64
	}
	dir := *jobFlag
	switch {
	case dir == "" && fs.NArg() == 1:
		dir = fs.Arg(0)
	case dir != "" && fs.NArg() == 0:
	default:
		fmt.Fprintln(os.Stderr, usage)
		return nil, time.Time{}, 64
	}
	var now time.Time
	if *nowFlag != "" {
		t, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: --now: %v\n", name, err)
			return nil, time.Time{}, 64
		}
		now = t
	}
	j, err := model.LoadJob(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return nil, time.Time{}, 65
	}
	return j, now, 0
}

func encodeLines(w io.Writer, vs []lifecycle.Verdict) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, v := range vs {
		_ = enc.Encode(v) // one JSON object per line, like gate validate
	}
}

func encodeIndented(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// runSeal is `longform manifest seal`. Exit 0 sealed (or already past
// approved), 64 usage, 65 cells without a post (verdict lines on stdout) or
// a job not at approved, 70 write failure.
func runSeal(args []string) int {
	var allow *bool
	j, now, code := lifecycleFlags("manifest seal", sealUsage, args, func(fs *flag.FlagSet) {
		allow = fs.Bool("allow-missing", false, "seal even though some cells have no post; record them under job.extra.seal.missing")
	})
	if code != 0 {
		return code
	}
	res, err := lifecycle.Seal(j, lifecycle.SealOptions{AllowMissing: *allow, Now: now})
	switch {
	case errors.Is(err, lifecycle.ErrMissing):
		encodeLines(os.Stdout, res.Missing)
		fmt.Fprintf(os.Stderr, "seal: %d of %d approved cells have no recorded post; %s stays approved. Fix them (or pass --allow-missing if you decided to skip them) and re-run.\n", len(res.Missing), res.Cells, j.ID)
		return 65
	case errors.Is(err, lifecycle.ErrState), errors.Is(err, lifecycle.ErrData):
		fmt.Fprintln(os.Stderr, "seal:", err)
		return 65
	case err != nil:
		fmt.Fprintln(os.Stderr, "seal:", err)
		return 70
	case res.Already != "":
		fmt.Fprintf(os.Stderr, "seal: %s is already %s; nothing to do\n", j.ID, res.Already)
		return 0
	}
	encodeIndented(os.Stdout, res)
	msg := fmt.Sprintf("seal: %s approved -> scheduled: %d of %d cells have a recorded post", j.ID, res.Posts, res.Cells)
	if len(res.Missing) > 0 {
		msg += fmt.Sprintf(", %d missing (allowed, recorded in job.extra.seal.missing)", len(res.Missing))
	}
	if len(res.Held) > 0 {
		msg += fmt.Sprintf("; %d held clips entered the backlog", len(res.Held))
	}
	fmt.Fprintln(os.Stderr, msg)
	return 0
}

// runMonitor is `longform monitor`. Exit 0 monitored (or already past
// scheduled), 64 usage, 65 wrong state, no manifest or no API key, 70 a
// schedule did not resolve (verdict lines on stdout) or a non-retryable API
// error, 75 a retryable API failure (429/5xx/network): try the next pass.
func runMonitor(args []string) int {
	j, now, code := lifecycleFlags("monitor", monitorUsage, args, nil)
	if code != 0 {
		return code
	}
	opts := lifecycle.MonitorOptions{Now: now}
	if j.State == model.StateScheduled {
		if c, err := newOpusClient(); err != nil {
			opts.ClientErr = err
		} else {
			opts.Client = c
		}
	}
	before := failureIDs(j.Extra[lifecycle.KeyMonitor])
	ctx, cancel := signalContext()
	defer cancel()
	res, err := lifecycle.Monitor(ctx, j, opts)
	switch {
	case errors.Is(err, lifecycle.ErrVerify):
		encodeLines(os.Stdout, res.Failures)
		fmt.Fprintf(os.Stderr, "monitor: %s: %d of %d schedules did not resolve; state stays scheduled. %s\n", j.ID, len(res.Failures), res.Posts, res.Summary())
		ids := make([]string, 0, len(res.Failures))
		for _, f := range res.Failures {
			ids = append(ids, f.ScheduleID)
		}
		if strings.Join(ids, ",") != strings.Join(before, ",") {
			p, perr := model.LoadProfile(j.Profile)
			if perr != nil {
				p = &model.Profile{Name: j.Profile}
			}
			if nerr := notify.Send(p, j, "monitor_failed", fmt.Sprintf("%d scheduled posts cannot be found in OpusClip: %s", len(ids), strings.Join(ids, ", "))); nerr != nil {
				fmt.Fprintln(os.Stderr, "monitor: notify:", nerr)
			}
		}
		return 70
	case errors.Is(err, lifecycle.ErrState), errors.Is(err, lifecycle.ErrNoClient), errors.Is(err, lifecycle.ErrData):
		fmt.Fprintln(os.Stderr, "monitor:", err)
		return 65
	case err != nil:
		fmt.Fprintln(os.Stderr, "monitor:", err)
		var ae *opusclip.APIError
		var ue *url.Error
		if (errors.As(err, &ae) && ae.Temporary()) || errors.As(err, &ue) || ctx.Err() != nil {
			return 75
		}
		return 70
	case res.Already != "":
		fmt.Fprintf(os.Stderr, "monitor: %s is already %s; nothing to do\n", j.ID, res.Already)
		return 0
	}
	encodeIndented(os.Stdout, res)
	fmt.Fprintf(os.Stderr, "monitor: %s scheduled -> monitored. %s\n", j.ID, res.Summary())
	if res.Overdue > 0 {
		fmt.Fprintf(os.Stderr, "monitor: WARNING %d schedules are still pending over an hour after their publish time; they have not gone out (see check.note in manifest.json)\n", res.Overdue)
	}
	if res.NotPending > 0 || res.ManifestOnly > 0 {
		fmt.Fprintln(os.Stderr, "monitor: not verified over REST: whether past posts went out, and the Buffer posts. Check them with opusclip_list_scheduled_posts and the Buffer queue in a Claude session.")
	}
	return 0
}

// failureIDs pulls the schedule ids out of a previous extra.monitor record so
// a repeated failure is not notified on every watcher pass.
func failureIDs(v any) []string {
	rec, _ := v.(map[string]any)
	var ids []string
	switch fs := rec["failures"].(type) {
	case []any:
		for _, f := range fs {
			if m, ok := f.(map[string]any); ok {
				s, _ := m["schedule_id"].(string)
				ids = append(ids, s)
			}
		}
	case []lifecycle.Verdict:
		for _, f := range fs {
			ids = append(ids, f.ScheduleID)
		}
	}
	sort.Strings(ids)
	return ids
}

// runClose is `longform close`. Exit 0 closed, already closed, or the
// backlog still running (state unchanged), 64 usage, 65 wrong state, no
// profile or no scheduled_at, 70 write failure.
func runClose(args []string) int {
	j, now, code := lifecycleFlags("close", closeUsage, args, nil)
	if code != 0 {
		return code
	}
	if j.State == model.StateClosed {
		fmt.Fprintf(os.Stderr, "close: %s is already closed; nothing to do\n", j.ID)
		return 0
	}
	p, err := model.LoadProfile(j.Profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "close: profile:", err)
		return 65
	}
	res, err := lifecycle.Close(j, lifecycle.CloseOptions{BacklogDays: p.BacklogDays, Now: now})
	switch {
	case errors.Is(err, lifecycle.ErrState), errors.Is(err, lifecycle.ErrData):
		fmt.Fprintln(os.Stderr, "close:", err)
		return 65
	case err != nil:
		fmt.Fprintln(os.Stderr, "close:", err)
		return 70
	}
	encodeIndented(os.Stdout, res)
	ends := res.BacklogEnds
	if t, err := time.Parse(time.RFC3339, ends); err == nil {
		ends = t.UTC().Format("2006-01-02")
	}
	if res.Closed {
		fmt.Fprintf(os.Stderr, "close: %s monitored -> closed (backlog ended %s, %d held clips expired)\n", j.ID, ends, res.Expired)
	} else {
		fmt.Fprintf(os.Stderr, "close: %s stays monitored: %d held clips, backlog ends %s\n", j.ID, res.Held, ends)
	}
	return 0
}
