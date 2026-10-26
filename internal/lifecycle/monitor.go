package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/manifest"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/opusclip"
)

// Check methods and per-post statuses recorded in manifest.json.
const (
	MethodREST         = "opusclip-rest"
	MethodManifestOnly = "manifest-only"

	StatusPending      = "pending"       // listed by the scheduler: verified
	StatusNotPending   = "not_pending"   // publish time passed and no longer listed: posted, failed or cancelled; not verified
	StatusMissing      = "missing"       // publish time ahead but not listed: a failure
	StatusReplaced     = "replaced"      // not listed, but a later post for the same clip and platform is pending
	StatusManifestOnly = "manifest_only" // Buffer post: id present, nothing read back
)

// OverdueAfter is how long past its publish time a schedule may still be
// listed as pending before monitor calls it overdue. The 2026-10-25 probe
// found schedules still pending eight months after their publish time: they
// never fired.
const OverdueAfter = time.Hour

// ErrVerify is returned when at least one schedule does not resolve.
var ErrVerify = errors.New("schedules did not verify")

// ErrNoClient is returned when OpusClip posts need checking and no API
// client could be built (OPUSCLIP_API_KEY unset).
var ErrNoClient = errors.New("no OpusClip client")

// ScheduleLister is the one read the monitor needs; *opusclip.Client has it.
type ScheduleLister interface {
	ClipSchedules(ctx context.Context, projectID, clipID string) ([]opusclip.PendingSchedule, error)
}

// MonitorOptions tunes Monitor.
type MonitorOptions struct {
	Client    ScheduleLister // nil when no key is configured
	ClientErr error          // why Client is nil, for the error message
	Now       time.Time
}

// MonitorResult is what `longform monitor` prints and records under
// job.extra.monitor.
type MonitorResult struct {
	Already      model.State `json:"already,omitempty"`
	Method       string      `json:"method"`
	CheckedAt    string      `json:"checked_at"`
	Posts        int         `json:"posts"` // distinct ids checked
	Pending      int         `json:"pending"`
	NotPending   int         `json:"not_pending"`
	ManifestOnly int         `json:"manifest_only"`
	Overdue      int         `json:"overdue,omitempty"`       // pending although the publish time passed over OverdueAfter ago
	Duplicates   int         `json:"duplicate_ids,omitempty"` // repeated appends of one id, checked once
	Failures     []Verdict   `json:"failures"`
	Advanced     bool        `json:"advanced"`
}

// Monitor reads every OpusClip scheduleId in manifest.json back from the
// scheduler (GET /publish-schedules?q=findByProjectAndClip, once per clip),
// records each post's status and the scheduler's entry verbatim in
// manifest.json, and advances scheduled -> monitored when nothing is
// missing. The REST list only holds schedules still pending, so a schedule
// whose publish time has passed and that is no longer listed is recorded as
// not_pending (posted, failed or cancelled: the API cannot tell), never as
// verified. Buffer posts are checked against the manifest only.
func Monitor(ctx context.Context, j *model.Job, opts MonitorOptions) (*MonitorResult, error) {
	now := nowOr(opts.Now)
	res := &MonitorResult{Failures: []Verdict{}, CheckedAt: rfc3339(now)}
	if j.State == model.StateFailed {
		return res, fmt.Errorf("%w: job %s is failed (%s)", ErrState, j.ID, j.Error)
	}
	if j.AtLeast(model.StateMonitored) {
		res.Already = j.State
		return res, nil
	}
	if j.State != model.StateScheduled {
		return res, fmt.Errorf("%w: monitor needs a scheduled job; %s is %s (seal it first: longform manifest seal --job %s)", ErrState, j.ID, j.State, j.Dir())
	}
	m, existed, err := loadManifest(j)
	if err != nil {
		return res, err
	}
	if !existed {
		return res, fmt.Errorf("%w: %s has no manifest.json", ErrState, j.ID)
	}

	type postRef struct{ clip, post int }
	type group struct {
		project, clip string
		ids           map[string][]postRef
	}
	groups := map[string]*group{}
	seen := map[string][]postRef{} // schedule id -> every post carrying it
	var opusIDs, bufferIDs []string
	projects := projectIndex(j)
	for ci, c := range m.Clips {
		for pi, p := range c.ScheduledPosts {
			if p.Error != "" || p.ScheduleID == "" {
				continue // a failed attempt, or a cell seal already recorded as missing
			}
			ref := postRef{ci, pi}
			if prev := seen[p.ScheduleID]; len(prev) > 0 {
				seen[p.ScheduleID] = append(prev, ref)
				res.Duplicates++
				continue
			}
			seen[p.ScheduleID] = []postRef{ref}
			if !p.IsOpusClip() {
				bufferIDs = append(bufferIDs, p.ScheduleID)
				continue
			}
			opusIDs = append(opusIDs, p.ScheduleID)
			project := p.ProjectID
			if project == "" {
				project = projects[c.ClipID]
			}
			if project == "" {
				res.Failures = append(res.Failures, Verdict{ClipID: c.ClipID, Label: p.Label, Platform: p.Platform, ScheduleID: p.ScheduleID, Field: "project_id",
					Reason: "no project id for this clip in the post, review/schedule.json or clips.json, so the schedule cannot be looked up"})
				continue
			}
			key := project + "." + c.ClipID
			g := groups[key]
			if g == nil {
				g = &group{project: project, clip: c.ClipID, ids: map[string][]postRef{}}
				groups[key] = g
			}
			g.ids[p.ScheduleID] = nil
		}
	}
	res.Posts = len(opusIDs) + len(bufferIDs)
	res.Method = MethodManifestOnly
	if len(opusIDs) > 0 {
		res.Method = MethodREST
	}

	checks := map[string]manifest.PostCheck{}
	for _, id := range bufferIDs {
		checks[id] = manifest.PostCheck{CheckedAt: res.CheckedAt, Method: MethodManifestOnly, Status: StatusManifestOnly,
			Note: "Buffer post: the id is recorded; the pipeline cannot read Buffer back"}
		res.ManifestOnly++
	}
	if len(groups) > 0 && opts.Client == nil {
		why := "OPUSCLIP_API_KEY is not set"
		if opts.ClientErr != nil {
			why = opts.ClientErr.Error()
		}
		return res, fmt.Errorf("%w: %d OpusClip schedules need reading back: %s", ErrNoClient, len(opusIDs), why)
	}
	type candidate struct {
		key string // clip/platform
		v   Verdict
	}
	var missing []candidate
	pendingFor := map[string]bool{} // clip/platform with a pending schedule
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := groups[k]
		listed, err := opts.Client.ClipSchedules(ctx, g.project, g.clip)
		if err != nil {
			return res, fmt.Errorf("read schedules of %s.%s: %w", g.project, g.clip, err)
		}
		byID := map[string]opusclip.PendingSchedule{}
		for _, s := range listed {
			byID[s.ScheduleID] = s
		}
		for id := range g.ids {
			ref := seen[id][0]
			p := m.Clips[ref.clip].ScheduledPosts[ref.post]
			chk := manifest.PostCheck{CheckedAt: res.CheckedAt, Method: MethodREST}
			if s, ok := byID[id]; ok {
				chk.Status, chk.Response = StatusPending, s.Raw
				res.Pending++
				pendingFor[p.ClipID+"/"+p.Platform] = true
				due := s.PublishAt
				if due == "" {
					due = p.ScheduledAtUTC
				}
				if at, err := time.Parse(time.RFC3339, due); err == nil && now.Sub(at) > OverdueAfter {
					chk.Note = fmt.Sprintf("still pending %s after its publish time %s: it has not gone out (check the account connection in OpusClip)", now.Sub(at).Round(time.Minute), due)
					res.Overdue++
				}
				checks[id] = chk
				continue
			}
			at, perr := time.Parse(time.RFC3339, p.ScheduledAtUTC)
			switch {
			case perr == nil && !at.After(now):
				chk.Status = StatusNotPending
				chk.Note = "publish time has passed and the scheduler no longer lists it: posted, failed or cancelled (the REST key cannot tell which; opusclip_list_scheduled_posts in a Claude session can)"
				res.NotPending++
			default:
				chk.Status = StatusMissing
				reason := fmt.Sprintf("not among the pending schedules of %s.%s although its publish time %s is still ahead", g.project, g.clip, p.ScheduledAtUTC)
				if perr != nil {
					reason = fmt.Sprintf("not among the pending schedules of %s.%s, and scheduled_at_utc %q gives no publish time to judge it by", g.project, g.clip, p.ScheduledAtUTC)
				}
				chk.Note = reason
				missing = append(missing, candidate{p.ClipID + "/" + p.Platform, Verdict{ClipID: p.ClipID, Label: p.Label, Platform: p.Platform, ScheduleID: id, Field: "schedule_id", Reason: reason,
					FixHint: "check the post with opusclip_list_scheduled_posts; re-schedule the cell with longform opus schedule, without --job (the manifest still records this id, so --job would skip the cell), and append its line (the new pending id replaces this one), then re-run longform monitor"}})
			}
			checks[id] = chk
		}
	}
	// A cell re-scheduled after its first schedule vanished carries two
	// posts; the pending one stands for the cell and the old id is replaced.
	for _, c := range missing {
		if pendingFor[c.key] {
			chk := checks[c.v.ScheduleID]
			chk.Status = StatusReplaced
			chk.Note = "no longer listed; a later schedule for the same clip and platform is pending and replaces it"
			checks[c.v.ScheduleID] = chk
			continue
		}
		res.Failures = append(res.Failures, c.v)
	}
	for id, refs := range seen {
		chk, ok := checks[id]
		if !ok {
			continue
		}
		for _, r := range refs {
			c := chk
			m.Clips[r.clip].ScheduledPosts[r.post].Check = &c
		}
	}
	if err := m.Save(manifest.PathIn(j.Dir())); err != nil {
		return res, err
	}
	sort.Slice(res.Failures, func(a, b int) bool { return res.Failures[a].ScheduleID < res.Failures[b].ScheduleID })

	record := map[string]any{"method": res.Method, "checked_at": res.CheckedAt, "posts": res.Posts, "pending": res.Pending, "not_pending": res.NotPending, "manifest_only": res.ManifestOnly}
	if res.Overdue > 0 {
		record["overdue"] = res.Overdue
	}
	if len(res.Failures) > 0 {
		record["failures"] = res.Failures
		setExtra(j, KeyMonitor, record)
		if err := j.Save(); err != nil {
			return res, err
		}
		return res, fmt.Errorf("%w: %d of %d", ErrVerify, len(res.Failures), res.Posts)
	}
	setExtra(j, KeyMonitor, record)
	setExtra(j, KeyMonitoredAt, res.CheckedAt)
	if err := j.Advance(model.StateMonitored, "monitor", res.Summary()); err != nil {
		return res, err
	}
	res.Advanced = true
	return res, nil
}

// Summary is the one-line account of what was and was not verified.
func (r *MonitorResult) Summary() string {
	var parts []string
	if r.Pending+r.NotPending > 0 || r.Method == MethodREST {
		parts = append(parts, fmt.Sprintf("%s: %d OpusClip schedules read back pending via GET /publish-schedules?q=findByProjectAndClip", r.Method, r.Pending))
		if r.Overdue > 0 {
			parts = append(parts, fmt.Sprintf("%d of them still pending over an hour after their publish time (overdue: they have not gone out)", r.Overdue))
		}
		if r.NotPending > 0 {
			parts = append(parts, fmt.Sprintf("%d past their publish time and no longer listed (posted, failed or cancelled; not verified)", r.NotPending))
		}
	}
	if r.ManifestOnly > 0 {
		parts = append(parts, fmt.Sprintf("%d Buffer posts checked against the manifest only", r.ManifestOnly))
	}
	if len(parts) == 0 {
		return "manifest-only: no posts recorded"
	}
	return strings.Join(parts, "; ")
}
