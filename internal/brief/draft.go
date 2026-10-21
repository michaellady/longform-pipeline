// Package brief reads the skill's review/draft.json, renders review/brief.md,
// and parses the approval grammar into review/decision.json plus the derived
// review/schedule.json cells. It holds no editorial judgment: every choice in
// the brief was made by the skill and is only laid out here.
package brief

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DraftFile is the job-relative path the skill writes.
const DraftFile = "review/draft.json"

// Draft is the contract documented in docs/draft-schema.md.
type Draft struct {
	SchemaVersion string        `json:"schema_version"`
	JobID         string        `json:"job_id"`
	Profile       string        `json:"profile"`
	FullVideo     FullVideo     `json:"full_video"`
	Clips         []Clip        `json:"clips"`
	Statics       []Static      `json:"statics"`
	Censored      []Censored    `json:"censored"`
	Stabilization Stabilization `json:"stabilization"`
	Credits       Credits       `json:"credits"`
	Decisions     []Decision    `json:"decisions"`
}

// FullVideo is the long-form package that goes to every surface.
type FullVideo struct {
	ProjectID   string      `json:"project_id"`
	ClipID      string      `json:"clip_id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Chapters    []Chapter   `json:"chapters"`
	Thumbnails  []Thumbnail `json:"thumbnails"`
	Platforms   []string    `json:"platforms"`
}

// Chapter is one YouTube chapter marker.
type Chapter struct {
	AtMs  int64  `json:"at_ms"`
	Title string `json:"title"`
}

// Thumbnail is one candidate; exactly one should be chosen.
type Thumbnail struct {
	Path   string `json:"path"` // job-relative
	Chosen bool   `json:"chosen"`
}

// Clip is one OpusClip short with its per-platform cells.
type Clip struct {
	ProjectID   string  `json:"project_id"`
	ClipID      string  `json:"clip_id"`
	Score       float64 `json:"score"`
	DurationSec float64 `json:"duration_sec"`
	ShortTitle  string  `json:"short_title"`
	Status      string  `json:"status"` // "post" | "held"
	PreviewURL  string  `json:"preview_url"`
	Cells       []Cell  `json:"cells"`
}

// Cell is one post on one platform.
type Cell struct {
	Platform     string   `json:"platform"`
	AccountID    string   `json:"account_id"`
	SubAccountID string   `json:"sub_account_id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Hashtags     []string `json:"hashtags"`
	PublishAt    string   `json:"publish_at"` // RFC3339; empty = not slotted
}

// Static is one static post to render from a template.
type Static struct {
	ID        string       `json:"id"`
	Format    string       `json:"format"` // quote | carousel | still | frame_carousel
	Platforms []string     `json:"platforms"`
	Caption   string       `json:"caption"`
	Inputs    StaticInputs `json:"inputs"`
}

// StaticInputs are the template fields; which ones matter depends on Format.
type StaticInputs struct {
	Quote       string  `json:"quote,omitempty"`
	Attribution string  `json:"attribution,omitempty"`
	TimestampMs int64   `json:"timestamp_ms,omitempty"`
	Slides      []Slide `json:"slides,omitempty"`
}

// Slide is one carousel panel.
type Slide struct {
	Kind        string `json:"kind"` // hook | section | quote | stat | cta
	Text        string `json:"text"`
	Sub         string `json:"sub"`
	TimestampMs int64  `json:"timestamp_ms,omitempty"`
}

// Censored is one beeped span.
type Censored struct {
	Word    string `json:"word"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
}

// Stabilization is the before/after frame pair.
type Stabilization struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// Credits is OpusClip spend.
type Credits struct {
	Spent   int `json:"spent"`
	ToSpend int `json:"to_spend"`
}

// Decision is one question the skill could not settle alone.
type Decision struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Default  string   `json:"default"`
}

// LoadDraft reads <jobDir>/review/draft.json.
func LoadDraft(jobDir string) (*Draft, error) {
	b, err := os.ReadFile(filepath.Join(jobDir, DraftFile))
	if err != nil {
		return nil, err
	}
	var d Draft
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", DraftFile, err)
	}
	if d.SchemaVersion == "" {
		return nil, fmt.Errorf("%s: schema_version is required", DraftFile)
	}
	for i, c := range d.Clips {
		switch c.Status {
		case "post", "held":
		case "":
			d.Clips[i].Status = "post"
		default:
			return nil, fmt.Errorf("%s: clips[%d].status must be post or held, got %q", DraftFile, i, c.Status)
		}
	}
	return &d, nil
}

// PublishTime parses the cell's publish_at; ok is false when unset or invalid.
func (c Cell) PublishTime() (time.Time, bool) {
	if c.PublishAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, c.PublishAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// FirstPublish is the clip's earliest slotted cell time.
func (c Clip) FirstPublish() (time.Time, bool) {
	var best time.Time
	found := false
	for _, cell := range c.Cells {
		if t, ok := cell.PublishTime(); ok && (!found || t.Before(best)) {
			best, found = t, true
		}
	}
	return best, found
}

// OrderedClips returns indices into d.Clips in brief-table order: by first
// publish_at ascending (unslotted last), then score descending, then clip id.
// Row n of the table is OrderedClips()[n-1]; render and parse-decision share
// this so row numbers in the grammar mean the same clip the reader saw.
func (d *Draft) OrderedClips() []int {
	idx := make([]int, len(d.Clips))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ca, cb := d.Clips[idx[a]], d.Clips[idx[b]]
		ta, oka := ca.FirstPublish()
		tb, okb := cb.FirstPublish()
		if oka != okb {
			return oka // slotted before unslotted
		}
		if oka && !ta.Equal(tb) {
			return ta.Before(tb)
		}
		if ca.Score != cb.Score {
			return ca.Score > cb.Score
		}
		return ca.ClipID < cb.ClipID
	})
	return idx
}

// Platforms lists every platform the draft touches, sorted and unique.
func (d *Draft) Platforms() []string {
	set := map[string]bool{}
	for _, p := range d.FullVideo.Platforms {
		set[p] = true
	}
	for _, c := range d.Clips {
		for _, cell := range c.Cells {
			set[cell.Platform] = true
		}
	}
	for _, s := range d.Statics {
		for _, p := range s.Platforms {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ChosenThumbnail is the 1-based index of the chosen thumbnail, 0 if none.
func (d *Draft) ChosenThumbnail() int {
	for i, t := range d.FullVideo.Thumbnails {
		if t.Chosen {
			return i + 1
		}
	}
	return 0
}

// PlatformMatches reports whether a held platform name covers a cell's
// platform: exact, or a family prefix (`linkedin` holds linkedin_page and
// linkedin_profile).
func PlatformMatches(held, platform string) bool {
	held = strings.ToLower(held)
	platform = strings.ToLower(platform)
	return platform == held || strings.HasPrefix(platform, held+"_")
}

// FormatMs renders milliseconds as mm:ss (h:mm:ss past an hour).
func FormatMs(ms int64) string {
	s := ms / 1000
	h, m, sec := s/3600, (s%3600)/60, s%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}
