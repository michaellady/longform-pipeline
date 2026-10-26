package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/gates"
	"github.com/michaellady/longform-pipeline/internal/manifest"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// ErrMissing is returned by Seal when an approved cell has no recorded post.
var ErrMissing = errors.New("approved cells without a recorded post")

// SealOptions tunes Seal.
type SealOptions struct {
	// AllowMissing seals anyway and records the missing cells under
	// job.extra.seal.missing (the user decided to skip them).
	AllowMissing bool
	Now          time.Time
}

// SealResult reports what Seal found and did.
type SealResult struct {
	Already  model.State `json:"already,omitempty"` // set when the job was past approved
	Cells    int         `json:"cells"`             // cells that needed a post (held/skipped excluded)
	Posts    int         `json:"posts"`             // cells matched to a recorded post
	Missing  []Verdict   `json:"missing"`
	Held     []string    `json:"held_clips"` // clip ids recorded as held (the backlog)
	Advanced bool        `json:"advanced"`
}

// Seal checks that every approved cell in review/schedule.json has a
// recorded post in manifest.json (an OpusClip scheduleId, or the Buffer post
// id a static's line named), records the clips left in the backlog as held,
// and advances approved -> scheduled. Idempotent: a job already scheduled or
// later is reported, not changed.
func Seal(j *model.Job, opts SealOptions) (*SealResult, error) {
	res := &SealResult{Missing: []Verdict{}, Held: []string{}}
	if j.State == model.StateFailed {
		return res, fmt.Errorf("%w: job %s is failed (%s)", ErrState, j.ID, j.Error)
	}
	if j.AtLeast(model.StateScheduled) {
		res.Already = j.State
		return res, nil
	}
	if j.State != model.StateApproved {
		return res, fmt.Errorf("%w: seal needs an approved job; %s is %s", ErrState, j.ID, j.State)
	}
	cells, err := loadCells(j)
	if err != nil {
		return res, err
	}
	m, existed, err := loadManifest(j)
	if err != nil {
		return res, err
	}
	res.Cells, res.Posts, res.Missing = matchCells(cells, m)
	if len(res.Missing) > 0 && !opts.AllowMissing {
		return res, fmt.Errorf("%w: %d of %d", ErrMissing, len(res.Missing), res.Cells)
	}

	held, err := backlogClips(j, cells, m)
	if err != nil {
		return res, err
	}
	now := nowOr(opts.Now)
	if !existed {
		m.CreatedAt = rfc3339(now)
		m.ProjectID = j.OpusClip.ShortsProjectID
		m.SourceVideo = &manifest.SourceVideo{ID: j.ID, Profile: j.Profile}
		if j.Delivery != nil {
			m.SourceVideo.DurationSeconds = float64(j.Delivery.DurationMs) / 1000
		}
	}
	for _, c := range held {
		if m.EnsureClip(c) {
			res.Held = append(res.Held, c.ClipID)
		}
	}
	if !existed || len(res.Held) > 0 {
		if err := m.Save(manifest.PathIn(j.Dir())); err != nil {
			return res, err
		}
	}

	record := map[string]any{"sealed_at": rfc3339(now), "cells": res.Cells, "posts": res.Posts, "held_clips": len(m.HeldClips())}
	if len(res.Missing) > 0 {
		record["missing"] = res.Missing
	}
	setExtra(j, KeySeal, record)
	setExtra(j, KeyScheduledAt, rfc3339(now))
	note := fmt.Sprintf("%d posts sealed", res.Posts)
	if len(res.Missing) > 0 {
		note += fmt.Sprintf("; %d cells missing, allowed by --allow-missing", len(res.Missing))
	}
	if err := j.Advance(model.StateScheduled, "manifest seal", note); err != nil {
		return res, err
	}
	res.Advanced = true
	return res, nil
}

// matchCells pairs every cell that should have posted with one recorded post
// of the same clip id and platform that has an id and no error. Each post
// satisfies at most one cell, so a double append never hides a missing one.
func matchCells(cells []gates.Cell, m *manifest.Manifest) (need, matched int, missing []Verdict) {
	type ref struct{ clip, post int }
	used := map[ref]bool{}
	missing = []Verdict{}
	for _, c := range cells {
		if c.Parked() {
			continue
		}
		need++
		platform := gates.PlatformOf(c)
		var errored, noID string
		found := false
		for ci := range m.Clips {
			if m.Clips[ci].ClipID != c.ClipID {
				continue
			}
			for pi, p := range m.Clips[ci].ScheduledPosts {
				pp := p.Platform
				if pp == "" {
					pp = gates.LabelToPlatform(p.Label)
				}
				if pp != platform || used[ref{ci, pi}] {
					continue
				}
				switch {
				case p.Error != "":
					errored = p.Error
				case p.ScheduleID == "":
					noID = "a post is recorded without an id"
				default:
					used[ref{ci, pi}] = true
					found = true
				}
				if found {
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			matched++
			continue
		}
		v := Verdict{Kind: c.Kind, ClipID: c.ClipID, Label: c.Label, Platform: platform, Field: "post"}
		switch {
		case noID != "":
			v.Reason = noID
		case errored != "":
			v.Reason = "the schedule call failed: " + errored
		default:
			v.Reason = "no post recorded in manifest.json"
		}
		if c.Kind == gates.KindStatic {
			v.FixHint = `post it through Buffer, then append {"cell":<cell>,"response":<Buffer reply>,"transport":"buffer","post_id":"<Buffer post id>"} with longform manifest append-post`
		} else {
			v.FixHint = "schedule this cell with longform opus schedule and pipe its line into longform manifest append-post"
		}
		missing = append(missing, v)
	}
	return need, matched, missing
}

// backlogClips lists the clips that stay in the backlog: held in the draft or
// by `approve except`, or with only held cells in the schedule; never a
// dropped row and never a clip that posted. They enter the manifest as held
// so `manifest expire` and `close` can count them down.
func backlogClips(j *model.Job, cells []gates.Cell, m *manifest.Manifest) ([]manifest.Clip, error) {
	posted := map[string]bool{}
	for _, c := range m.Clips {
		for _, p := range c.ScheduledPosts {
			if p.ScheduleID != "" && p.Error == "" {
				posted[c.ClipID] = true
			}
		}
	}
	d, err := brief.LoadDraft(j.Dir())
	if errors.Is(err, os.ErrNotExist) {
		d = nil
	} else if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrData, err)
	}
	var rec brief.DecisionRecord
	if raw, err := os.ReadFile(j.Path(brief.DecisionFile)); err == nil {
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrData, brief.DecisionFile, err)
		}
	}
	in := func(rows []int, row int) bool {
		for _, r := range rows {
			if r == row {
				return true
			}
		}
		return false
	}
	byID := map[string]manifest.Clip{}
	dropped := map[string]bool{}
	if d != nil {
		for row0, idx := range d.OrderedClips() {
			c := d.Clips[idx]
			row := row0 + 1
			if in(rec.DroppedClips, row) {
				dropped[c.ClipID] = true
				continue
			}
			if c.Status == "held" || in(rec.HeldClips, row) {
				score := c.Score
				byID[c.ClipID] = manifest.Clip{ClipID: c.ClipID, Title: c.ShortTitle, Score: &score, DurationSec: c.DurationSec, Status: manifest.StatusHeld}
			}
		}
	}
	live := map[string]bool{}
	for _, c := range cells {
		if !c.Parked() {
			live[c.ClipID] = true
		}
	}
	for _, c := range cells {
		if c.Held() && c.Kind == gates.KindClip && !live[c.ClipID] && !dropped[c.ClipID] {
			if _, ok := byID[c.ClipID]; !ok {
				byID[c.ClipID] = manifest.Clip{ClipID: c.ClipID, Title: c.Title, DurationSec: c.DurationSec, Status: manifest.StatusHeld}
			}
		}
	}
	var out []manifest.Clip
	for id, c := range byID {
		if !posted[id] {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ClipID < out[b].ClipID })
	return out, nil
}
