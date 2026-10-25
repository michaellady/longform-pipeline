package lifecycle

import (
	"fmt"
	"time"

	"github.com/michaellady/longform-pipeline/internal/manifest"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// CloseOptions tunes Close.
type CloseOptions struct {
	BacklogDays int // profile.backlog_days
	Now         time.Time
}

// CloseResult reports where the backlog stands.
type CloseResult struct {
	Already     model.State `json:"already,omitempty"`
	State       model.State `json:"state"`
	Held        int         `json:"held"`    // clips still held (not yet expired)
	Expired     int         `json:"expired"` // clips the backlog expired
	BacklogEnds string      `json:"backlog_ends"`
	Closed      bool        `json:"closed"`
}

// Close advances monitored -> closed once scheduled_at + backlog_days has
// passed and no held clip remains (run `manifest expire` first). Before that
// it changes nothing and reports the held count and the end date.
func Close(j *model.Job, opts CloseOptions) (*CloseResult, error) {
	res := &CloseResult{State: j.State}
	if j.State == model.StateClosed {
		res.Already = j.State
		return res, nil
	}
	if j.State != model.StateMonitored {
		return res, fmt.Errorf("%w: close needs a monitored job; %s is %s", ErrState, j.ID, j.State)
	}
	at, ok := ScheduledAt(j)
	if !ok {
		return res, fmt.Errorf("%w: %s has no extra.scheduled_at and no scheduled transition", ErrState, j.ID)
	}
	m, existed, err := loadManifest(j)
	if err != nil {
		return res, err
	}
	if !existed {
		return res, fmt.Errorf("%w: %s has no manifest.json", ErrState, j.ID)
	}
	for _, c := range m.Clips {
		switch c.Status {
		case manifest.StatusHeld:
			res.Held++
		case manifest.StatusExpired:
			res.Expired++
		}
	}
	ends := at.Add(time.Duration(opts.BacklogDays) * 24 * time.Hour)
	res.BacklogEnds = rfc3339(ends)
	now := nowOr(opts.Now)
	if now.Before(ends) || res.Held > 0 {
		return res, nil
	}
	setExtra(j, KeyClosedAt, rfc3339(now))
	note := fmt.Sprintf("backlog ended %s; %d held clips expired", ends.UTC().Format("2006-01-02"), res.Expired)
	if err := j.Advance(model.StateClosed, "close", note); err != nil {
		return res, err
	}
	res.State, res.Closed = j.State, true
	return res, nil
}
