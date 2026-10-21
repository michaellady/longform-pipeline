// Package gates is the deterministic pre-publish layer: the cells schema the
// pipeline hands from `slots propose` to `gate validate` to `opus schedule`,
// and the per-platform checks (caps, duration, profanity, cadence, timing)
// that refuse a cell before anything is fired. Pure transport: it never
// rewrites content and never calls an API.
//
// Vendored from github.com/michaellady/claude-social-media-skills
// _shared/publish-constraints (D24): the constraints table, the exit-65
// verdict contract and the label→platform mapping keep their semantics; the
// cell schema is widened to the longform pipeline's fields.
package gates

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// Cell kinds and statuses. See docs/cells-schema.md.
const (
	KindClip   = "clip"
	KindFull   = "full"
	KindStatic = "static"

	StatusHeld    = "held"    // past the backlog horizon; expires later
	StatusSkipped = "skipped" // platform has no connected account in the profile
)

// Platforms the pipeline knows. A cell whose platform is not in this set fails
// closed in the gate.
var Platforms = []string{
	"youtube", "instagram", "facebook", "linkedin_page", "linkedin_profile",
	"tiktok", "threads", "x", "bluesky", "pinterest",
}

// Cell is one (asset × platform) publish the pipeline is about to fire. The
// same JSON array flows through slots → gate → opus schedule unchanged except
// for publish_at and status.
type Cell struct {
	Kind         string   `json:"kind"`
	ClipID       string   `json:"clip_id"`
	ProjectID    string   `json:"project_id"`
	Platform     string   `json:"platform"`
	AccountID    string   `json:"account_id"`
	SubAccountID string   `json:"sub_account_id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Hashtags     []string `json:"hashtags"`
	DurationSec  float64  `json:"duration_sec"`
	PublishAt    string   `json:"publish_at"` // RFC3339 UTC; empty before slots run
	Label        string   `json:"label"`
	Status       string   `json:"status,omitempty"` // "" | "held"
}

// Held reports whether slots parked the cell past the backlog horizon.
func (c Cell) Held() bool { return c.Status == StatusHeld }

// Parked reports whether the cell will not fire in this run (held or
// skipped), so it needs no publish_at and takes no cadence slot.
func (c Cell) Parked() bool { return c.Status == StatusHeld || c.Status == StatusSkipped }

// PublishTime parses publish_at. An empty value is an error so callers never
// mistake "unscheduled" for the zero time.
func (c Cell) PublishTime() (time.Time, error) {
	if c.PublishAt == "" {
		return time.Time{}, fmt.Errorf("publish_at is empty")
	}
	t, err := time.Parse(time.RFC3339, c.PublishAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("publish_at %q: %w", c.PublishAt, err)
	}
	return t, nil
}

// CountsCadence reports whether the cell consumes one of the platform's daily
// cadence slots (D12): a scheduled clip or full-video post. Statics ride the
// Buffer queue at their own 1/day rule and are excluded.
func (c Cell) CountsCadence() bool {
	return !c.Parked() && c.PublishAt != "" && (c.Kind == KindClip || c.Kind == KindFull)
}

// ReadCells decodes a JSON array of cells.
func ReadCells(r io.Reader) ([]Cell, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	// Accept either a bare array (slots/gate/opus interchange) or the object
	// `brief parse-decision` writes to review/schedule.json ({cells: [...]}).
	var cells []Cell
	if err := json.Unmarshal(raw, &cells); err != nil {
		var wrapped struct {
			Cells []Cell `json:"cells"`
		}
		if err2 := json.Unmarshal(raw, &wrapped); err2 != nil || wrapped.Cells == nil {
			return nil, fmt.Errorf("not a JSON array of cells (nor an object with a cells array): %w", err)
		}
		cells = wrapped.Cells
	}
	for i := range cells {
		if cells[i].Hashtags == nil {
			cells[i].Hashtags = []string{}
		}
	}
	return cells, nil
}

// LoadCells reads a cells file. An empty path yields nil, nil so optional
// inputs (--existing) need no special casing.
func LoadCells(path string) ([]Cell, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cells, err := ReadCells(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cells, nil
}

// WriteCells encodes cells as an indented JSON array.
func WriteCells(w io.Writer, cells []Cell) error {
	if cells == nil {
		cells = []Cell{}
	}
	for i := range cells {
		if cells[i].Hashtags == nil {
			cells[i].Hashtags = []string{}
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(cells)
}

// LocalDay is the YYYY-MM-DD calendar day of t in loc.
func LocalDay(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2006-01-02")
}

// LocalSlot is the HH:MM wall-clock time of t in loc.
func LocalSlot(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("15:04")
}
