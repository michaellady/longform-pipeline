// Package manifest is the Go rewrite of claude-social-media-skills/
// _shared/post-manifest/post_manifest.sh (D24): the closed-loop ledger of
// every (clip × channel) schedule call, with the scheduler's response kept
// verbatim so a later fetcher can join platform analytics back to the
// source video. The shape is the vendored one plus schema_version,
// source_video.{duration_seconds,profile}, clips[].status and
// scheduled_posts[].{clip_id,platform,schedule_id}.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/gates"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// SchemaVersion is written on init; consumers branch on it.
const SchemaVersion = "1"

// Clip statuses.
const (
	StatusPost    = "post"
	StatusHeld    = "held"
	StatusExpired = "expired"
)

// Manifest is <job>/manifest.json.
type Manifest struct {
	SchemaVersion string       `json:"schema_version"`
	ProjectID     string       `json:"project_id,omitempty"`
	SourceVideo   *SourceVideo `json:"source_video,omitempty"`
	CreatedAt     string       `json:"created_at"`
	Clips         []Clip       `json:"clips"`
}

// SourceVideo links back to the upstream long-form video.
type SourceVideo struct {
	ID              string  `json:"id,omitempty"`
	Title           string  `json:"title,omitempty"`
	URL             string  `json:"url,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	Profile         string  `json:"profile,omitempty"`
}

// Clip is one curated unit and every schedule call made for it.
type Clip struct {
	ClipID         string          `json:"clip_id"`
	Title          string          `json:"title"`
	Description    string          `json:"description"`
	Score          *float64        `json:"score,omitempty"`
	DurationSec    float64         `json:"duration_sec,omitempty"`
	Theme          string          `json:"theme,omitempty"`
	Status         string          `json:"status"` // post | held | expired
	PublishedURL   string          `json:"published_url,omitempty"`
	CuratedAt      string          `json:"curated_at,omitempty"`
	ScheduledPosts []ScheduledPost `json:"scheduled_posts"`
}

// ScheduledPost is one (clip × channel) schedule record. api_response is the
// scheduler's reply verbatim; schedule_id is extracted from
// response.data.scheduleId at append time so readers need not know the
// scheduler's shape. For a Buffer post (transport "buffer") schedule_id holds
// the Buffer post id the appending line named in post_id.
type ScheduledPost struct {
	ClipID         string          `json:"clip_id"`
	ProjectID      string          `json:"project_id,omitempty"`
	Label          string          `json:"label"`
	Platform       string          `json:"platform,omitempty"`
	AccountID      string          `json:"account_id,omitempty"`
	SubAccountID   string          `json:"sub_account_id,omitempty"`
	Transport      string          `json:"transport,omitempty"` // "" = OpusClip; "buffer"
	ScheduledAtUTC string          `json:"scheduled_at_utc"`
	APIResponse    json.RawMessage `json:"api_response,omitempty"`
	ScheduleID     string          `json:"schedule_id,omitempty"`
	Error          string          `json:"error,omitempty"`
	Check          *PostCheck      `json:"check,omitempty"`
}

// Transports a scheduled post can name.
const (
	TransportOpusClip = "opusclip"
	TransportBuffer   = "buffer"
)

// IsOpusClip reports whether the post went through the OpusClip scheduler
// (an empty transport is OpusClip: every line before transport existed was).
func (p ScheduledPost) IsOpusClip() bool {
	return p.Transport == "" || p.Transport == TransportOpusClip
}

// PostCheck is the last `longform monitor` verdict on one post. response is
// the scheduler's list entry for the schedule, verbatim, when one was found.
type PostCheck struct {
	CheckedAt string          `json:"checked_at"`
	Method    string          `json:"method"` // opusclip-rest | manifest-only
	Status    string          `json:"status"` // pending | not_pending | missing | replaced | manifest_only
	Note      string          `json:"note,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`
}

// PathIn is the manifest file inside a job directory.
func PathIn(jobDir string) string { return filepath.Join(jobDir, "manifest.json") }

// Load reads a manifest. A missing file is os.ErrNotExist.
func Load(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m.Clips == nil {
		m.Clips = []Clip{}
	}
	for i := range m.Clips {
		if m.Clips[i].ScheduledPosts == nil {
			m.Clips[i].ScheduledPosts = []ScheduledPost{}
		}
	}
	return &m, nil
}

// Save writes the manifest atomically (temp + rename).
func (m *Manifest) Save(path string) error {
	if m.Clips == nil {
		m.Clips = []Clip{}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return model.WriteFileAtomic(path, append(b, '\n'), 0o644)
}

// InitOptions seed a new manifest.
type InitOptions struct {
	ProjectID string
	Source    SourceVideo
	Force     bool
	Now       time.Time
}

// Init creates the manifest. Idempotent: an existing file is left alone
// unless Force. Returns true when a file was written.
func Init(path string, o InitOptions) (bool, error) {
	if _, err := os.Stat(path); err == nil && !o.Force {
		return false, nil
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	m := &Manifest{SchemaVersion: SchemaVersion, ProjectID: o.ProjectID, CreatedAt: now.UTC().Format(time.RFC3339), Clips: []Clip{}}
	if o.Source != (SourceVideo{}) {
		src := o.Source
		m.SourceVideo = &src
	}
	return true, m.Save(path)
}

// Find returns the clip with id, or nil.
func (m *Manifest) Find(clipID string) *Clip {
	for i := range m.Clips {
		if m.Clips[i].ClipID == clipID {
			return &m.Clips[i]
		}
	}
	return nil
}

// EnsureClip appends a clip entry if none exists. Idempotent: an existing
// entry is untouched. Returns true when added.
func (m *Manifest) EnsureClip(c Clip) bool {
	if c.ClipID == "" || m.Find(c.ClipID) != nil {
		return false
	}
	if c.Status == "" {
		c.Status = StatusPost
	}
	if c.ScheduledPosts == nil {
		c.ScheduledPosts = []ScheduledPost{}
	}
	m.Clips = append(m.Clips, c)
	return true
}

// ErrNoClip is returned when a post targets a clip the manifest lacks.
var ErrNoClip = errors.New("clip not in manifest")

// AppendPost records one schedule call under its clip. The response is kept
// verbatim; a non-JSON body is wrapped as {"raw": "..."}; schedule_id is
// extracted from data.scheduleId when present.
func (m *Manifest) AppendPost(p ScheduledPost) error {
	c := m.Find(p.ClipID)
	if c == nil {
		return fmt.Errorf("%w: %s", ErrNoClip, p.ClipID)
	}
	p.APIResponse = normaliseResponse(p.APIResponse)
	if p.ScheduleID == "" {
		p.ScheduleID = ExtractScheduleID(p.APIResponse)
	}
	c.ScheduledPosts = append(c.ScheduledPosts, p)
	return nil
}

func normaliseResponse(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	if json.Valid(raw) {
		return raw
	}
	b, _ := json.Marshal(map[string]string{"raw": string(raw)})
	return b
}

// ExtractScheduleID pulls data.scheduleId out of an OpusClip response.
func ExtractScheduleID(raw json.RawMessage) string {
	var r struct {
		Data struct {
			ScheduleID string `json:"scheduleId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return ""
	}
	return r.Data.ScheduleID
}

// ScheduleLine is one JSON line emitted by `longform opus schedule`, or
// written by the skill for a Buffer post: {"cell", "response",
// "transport":"buffer", "post_id":"<Buffer post id>"}. The Buffer response
// shape is not wired here (it was never probed); the skill names the id.
type ScheduleLine struct {
	Cell       gates.Cell      `json:"cell"`
	Response   json.RawMessage `json:"response"`
	Error      string          `json:"error,omitempty"`
	ScheduleID string          `json:"schedule_id,omitempty"` // `opus schedule` echoes it
	Transport  string          `json:"transport,omitempty"`   // "" | opusclip | buffer
	PostID     string          `json:"post_id,omitempty"`     // explicit id; wins over the response
}

// AppendScheduleLine turns a schedule line into a clip entry (ensured from
// the cell) plus a scheduled post.
func (m *Manifest) AppendScheduleLine(l ScheduleLine) error {
	if l.Cell.ClipID == "" {
		return errors.New("schedule line has no cell.clip_id")
	}
	m.EnsureClip(Clip{ClipID: l.Cell.ClipID, Title: l.Cell.Title, Description: l.Cell.Description, DurationSec: l.Cell.DurationSec})
	id := l.PostID
	if id == "" && l.Error == "" {
		id = l.ScheduleID
	}
	return m.AppendPost(ScheduledPost{
		ClipID:         l.Cell.ClipID,
		ProjectID:      l.Cell.ProjectID,
		Label:          l.Cell.Label,
		Platform:       gates.PlatformOf(l.Cell),
		AccountID:      l.Cell.AccountID,
		SubAccountID:   l.Cell.SubAccountID,
		Transport:      l.Transport,
		ScheduledAtUTC: l.Cell.PublishAt,
		APIResponse:    l.Response,
		ScheduleID:     id,
		Error:          l.Error,
	})
}

// HeldClips returns the clips still waiting in the backlog (status held).
func (m *Manifest) HeldClips() []Clip {
	var out []Clip
	for _, c := range m.Clips {
		if c.Status == StatusHeld {
			out = append(out, c)
		}
	}
	return out
}

// CountScheduled is the total number of (clip × channel) records.
func (m *Manifest) CountScheduled() int {
	n := 0
	for _, c := range m.Clips {
		n += len(c.ScheduledPosts)
	}
	return n
}

// ListByChannel returns every post whose label contains needle
// (case-insensitive), each tagged with its clip id and title.
func (m *Manifest) ListByChannel(needle string) []ChannelPost {
	needle = strings.ToLower(needle)
	var out []ChannelPost
	for _, c := range m.Clips {
		for _, p := range c.ScheduledPosts {
			if strings.Contains(strings.ToLower(p.Label), needle) {
				out = append(out, ChannelPost{ScheduledPost: p, ClipTitle: c.Title})
			}
		}
	}
	if out == nil {
		out = []ChannelPost{}
	}
	return out
}

// ChannelPost is a scheduled post joined with its clip title.
type ChannelPost struct {
	ScheduledPost
	ClipTitle string `json:"clip_title"`
}

// Conflicts returns every post whose response says data.hasConflict.
func (m *Manifest) Conflicts() []ChannelPost {
	out := []ChannelPost{}
	for _, c := range m.Clips {
		for _, p := range c.ScheduledPosts {
			var r struct {
				Data struct {
					HasConflict bool `json:"hasConflict"`
				} `json:"data"`
			}
			if json.Unmarshal(p.APIResponse, &r) == nil && r.Data.HasConflict {
				out = append(out, ChannelPost{ScheduledPost: p, ClipTitle: c.Title})
			}
		}
	}
	return out
}

// ScheduleIDs lists every schedule id, stored or extractable.
func (m *Manifest) ScheduleIDs() []string {
	var out []string
	for _, c := range m.Clips {
		for _, p := range c.ScheduledPosts {
			id := p.ScheduleID
			if id == "" {
				id = ExtractScheduleID(p.APIResponse)
			}
			if id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

// Expire marks held clips whose curation time is more than days old as
// expired and returns them. The curation time is clips[].curated_at, else
// fallback (the job's clips.json curation time or job.CreatedAt).
func (m *Manifest) Expire(days int, fallback time.Time, now time.Time) []Clip {
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	var out []Clip
	for i := range m.Clips {
		c := &m.Clips[i]
		if c.Status != StatusHeld {
			continue
		}
		at := fallback
		if c.CuratedAt != "" {
			if t, err := time.Parse(time.RFC3339, c.CuratedAt); err == nil {
				at = t
			}
		}
		if at.IsZero() || !at.Before(cutoff) {
			continue
		}
		c.Status = StatusExpired
		out = append(out, *c)
	}
	return out
}
