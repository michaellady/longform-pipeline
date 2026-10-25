// Package lifecycle moves a job through the states after approval:
//
//	approved  -> scheduled  `longform manifest seal`: every approved cell has a recorded post
//	scheduled -> monitored  `longform monitor`: every OpusClip scheduleId is read back
//	monitored -> closed     `longform close`: the backlog window is over and nothing is held
//
// Pure transport (Primitive Test): it compares files and API answers and
// records what it saw; it never decides what to post.
package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/gates"
	"github.com/michaellady/longform-pipeline/internal/manifest"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// job.extra keys this package writes.
const (
	KeyScheduledAt = "scheduled_at" // RFC 3339 UTC, set by seal
	KeySeal        = "seal"         // {sealed_at, cells, posts, held_clips, missing?}
	KeyMonitor     = "monitor"      // {method, checked_at, posts, pending, not_pending, manifest_only, failures?}
	KeyMonitoredAt = "monitored_at" // RFC 3339 UTC, set when monitor advances
	KeyClosedAt    = "closed_at"    // RFC 3339 UTC, set when close advances
)

// ErrState is returned when a command runs on a job in the wrong state.
var ErrState = errors.New("wrong job state")

// ErrData is returned when a job file the command reads is missing or
// unparseable (review/schedule.json, manifest.json, draft.json, decision.json).
var ErrData = errors.New("unreadable job data")

// Verdict names one cell or post that did not pass, in the gate's style.
type Verdict struct {
	Kind       string `json:"kind,omitempty"`
	ClipID     string `json:"clip_id"`
	Label      string `json:"label,omitempty"`
	Platform   string `json:"platform"`
	ScheduleID string `json:"schedule_id,omitempty"`
	Field      string `json:"field"`
	Reason     string `json:"reason"`
	FixHint    string `json:"fix_hint,omitempty"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func nowOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

func setExtra(j *model.Job, key string, v any) {
	if j.Extra == nil {
		j.Extra = map[string]any{}
	}
	j.Extra[key] = v
}

// ScheduledAt is when seal moved the job to scheduled: extra.scheduled_at,
// else the history entry that reached scheduled.
func ScheduledAt(j *model.Job) (time.Time, bool) {
	if s, _ := j.Extra[KeyScheduledAt].(string); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
	}
	for i := len(j.History) - 1; i >= 0; i-- {
		if j.History[i].To == model.StateScheduled {
			return j.History[i].At, true
		}
	}
	return time.Time{}, false
}

// loadCells reads review/schedule.json, the cells parse-decision approved.
func loadCells(j *model.Job) ([]gates.Cell, error) {
	rel := j.Artifacts.Schedule
	if rel == "" {
		rel = brief.ScheduleFile
	}
	cells, err := gates.LoadCells(j.Path(rel))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrData, err)
	}
	return cells, nil
}

// loadManifest reads <job>/manifest.json; ok is false when it does not exist.
func loadManifest(j *model.Job) (*manifest.Manifest, bool, error) {
	m, err := manifest.Load(manifest.PathIn(j.Dir()))
	if errors.Is(err, os.ErrNotExist) {
		return &manifest.Manifest{SchemaVersion: manifest.SchemaVersion, Clips: []manifest.Clip{}}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrData, err)
	}
	return m, true, nil
}

// projectIndex maps clip id -> project id from review/schedule.json cells and
// clips.json, for manifests written before posts carried project_id.
func projectIndex(j *model.Job) map[string]string {
	idx := map[string]string{}
	if raw, err := os.ReadFile(filepath.Join(j.Dir(), "clips.json")); err == nil {
		var cf struct {
			Clips []struct {
				ProjectID string `json:"project_id"`
				ClipID    string `json:"clip_id"`
			} `json:"clips"`
		}
		if json.Unmarshal(raw, &cf) == nil {
			for _, c := range cf.Clips {
				if c.ClipID != "" && c.ProjectID != "" {
					idx[c.ClipID] = c.ProjectID
				}
			}
		}
	}
	if cells, err := loadCells(j); err == nil {
		for _, c := range cells {
			if c.ClipID != "" && c.ProjectID != "" {
				idx[c.ClipID] = c.ProjectID
			}
		}
	}
	return idx
}
