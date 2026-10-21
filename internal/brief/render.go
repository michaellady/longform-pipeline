package brief

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// BriefFile is the job-relative path of the rendered brief.
const BriefFile = "review/brief.md"

// GrammarHelp is the last line of every brief.
const GrammarHelp = "Reply with: `approve` · `approve except 3,7` · `hold linkedin` · `drop 4` · `swap thumbnail 2` · `decide d1 <option>` · `reject <note>` — join commands with `;`."

// Section headings in the fixed order D21 requires. Tests assert this order.
var SectionOrder = []string{
	"## Full video",
	"## Clips",
	"## Statics",
	"## Censored words",
	"## Stabilization",
	"## Decisions for you",
}

// StaticsIndex is <job>/statics/<id>/index.json as render-statics writes it.
type StaticsIndex struct {
	ID        string              `json:"id"`
	Format    string              `json:"format"`
	Files     []string            `json:"files"`     // relative to the static's dir
	Platforms map[string][]string `json:"platforms"` // platform -> files
}

// Render lays out brief.md from the job, profile (nil = defaults) and draft.
// Everything is read from the draft; the only filesystem access is a peek at
// statics/<id>/index.json to decide between rendered images and the plan.
func Render(job *model.Job, prof *model.Profile, d *Draft, now time.Time) string {
	loc := location(prof)
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	// Header.
	w("# Brief: %s\n\n", job.ID)
	w("- Profile: `%s`\n", job.Profile)
	if job.Delivery != nil {
		w("- Source duration: %s\n", FormatMs(job.Delivery.DurationMs))
	} else {
		w("- Source duration: unknown (no delivery probe)\n")
	}
	w("- Credits: %d spent, %d to spend\n", d.Credits.Spent, d.Credits.ToSpend)
	w("- Rendered: %s\n\n", now.In(loc).Format("Mon 2 Jan 2006 15:04 MST"))

	// Full video.
	w("## Full video\n\n")
	w("**%s**\n\n", d.FullVideo.Title)
	if d.FullVideo.Description != "" {
		w("%s\n\n", d.FullVideo.Description)
	}
	if len(d.FullVideo.Platforms) > 0 {
		w("Platforms: %s\n\n", strings.Join(d.FullVideo.Platforms, ", "))
	}
	if len(d.FullVideo.Chapters) > 0 {
		w("Chapters:\n\n")
		for _, c := range d.FullVideo.Chapters {
			w("- %s %s\n", FormatMs(c.AtMs), c.Title)
		}
		w("\n")
	}
	if len(d.FullVideo.Thumbnails) > 0 {
		w("Thumbnails:\n\n")
		for i, t := range d.FullVideo.Thumbnails {
			mark := ""
			if t.Chosen {
				mark = " (chosen)"
			}
			w("%d. ![thumbnail %d](%s)%s\n", i+1, i+1, t.Path, mark)
		}
		w("\n")
	}

	// Clip table.
	w("## Clips\n\n")
	if len(d.Clips) == 0 {
		w("No clips.\n\n")
	} else {
		w("| # | Score | Short title | Platforms | Slot (%s) | Preview | Status |\n", loc.String())
		w("|---|---|---|---|---|---|---|\n")
		for row, i := range d.OrderedClips() {
			c := d.Clips[i]
			plats, slots := clipColumns(c, loc)
			preview := ""
			if c.PreviewURL != "" {
				preview = fmt.Sprintf("[preview](%s)", c.PreviewURL)
			}
			status := "post"
			if c.Status == "held" {
				status = "hold"
			}
			w("| %d | %.0f | %s | %s | %s | %s | %s |\n", row+1, c.Score, cell(c.ShortTitle), plats, slots, preview, status)
		}
		w("\n")
	}

	// Statics grid.
	w("## Statics\n\n")
	if len(d.Statics) == 0 {
		w("No statics.\n\n")
	}
	for _, s := range d.Statics {
		w("### %s · %s · %s\n\n", s.ID, s.Format, strings.Join(s.Platforms, ", "))
		if ix := loadStaticsIndex(job.Dir(), s.ID); ix != nil && len(ix.Files) > 0 {
			var imgs, docs []string
			for _, f := range ix.Files {
				if strings.HasSuffix(f, ".png") {
					imgs = append(imgs, fmt.Sprintf("![%s](statics/%s/%s)", strings.TrimSuffix(f, ".png"), s.ID, f))
				} else {
					docs = append(docs, fmt.Sprintf("- [%s](statics/%s/%s)", f, s.ID, f))
				}
			}
			w("%s\n", strings.Join(imgs, " "))
			if len(docs) > 0 {
				w("\n%s\n", strings.Join(docs, "\n"))
			}
		} else {
			w("_Not rendered yet._ Planned: ")
			switch s.Format {
			case "quote":
				w("quote card — \"%s\" — %s", s.Inputs.Quote, s.Inputs.Attribution)
			case "still":
				w("frame still at %s", FormatMs(s.Inputs.TimestampMs))
			case "carousel", "frame_carousel":
				w("%d slides:", len(s.Inputs.Slides))
				for i, sl := range s.Inputs.Slides {
					w("\n  %d. [%s] %s", i+1, sl.Kind, sl.Text)
				}
			default:
				w("format %q", s.Format)
			}
			w("\n")
		}
		if s.Caption != "" {
			w("\nCaption: %s\n", s.Caption)
		}
		w("\n")
	}

	// Censored words.
	w("## Censored words\n\n")
	if len(d.Censored) == 0 {
		w("None.\n\n")
	} else {
		for _, c := range d.Censored {
			w("- `%s` at %s–%s\n", c.Word, FormatMs(c.StartMs), FormatMs(c.EndMs))
		}
		w("\n")
	}

	// Stabilization.
	w("## Stabilization\n\n")
	if d.Stabilization.Before == "" && d.Stabilization.After == "" {
		w("Not applied.\n\n")
	} else {
		w("| Before | After |\n|---|---|\n")
		w("| ![before](%s) | ![after](%s) |\n\n", d.Stabilization.Before, d.Stabilization.After)
	}

	// Decisions.
	w("## Decisions for you\n\n")
	if len(d.Decisions) == 0 {
		w("None. Everything above is ready to approve.\n\n")
	} else {
		for _, dec := range d.Decisions {
			w("- **%s** — %s\n", dec.ID, dec.Question)
			for _, o := range dec.Options {
				if o == dec.Default {
					w("  - %s (default)\n", o)
				} else {
					w("  - %s\n", o)
				}
			}
		}
		w("\n")
	}
	w("%s\n", GrammarHelp)
	return b.String()
}

// clipColumns returns the platform list and the unique local slot times, both
// in publish_at order.
func clipColumns(c Clip, loc *time.Location) (string, string) {
	cells := append([]Cell(nil), c.Cells...)
	sort.SliceStable(cells, func(a, b int) bool {
		ta, oka := cells[a].PublishTime()
		tb, okb := cells[b].PublishTime()
		if oka != okb {
			return oka
		}
		return ta.Before(tb)
	})
	var plats, slots []string
	seen := map[string]bool{}
	for _, cl := range cells {
		plats = append(plats, cl.Platform)
		if t, ok := cl.PublishTime(); ok {
			s := t.In(loc).Format("Mon 2 Jan 15:04")
			if !seen[s] {
				seen[s] = true
				slots = append(slots, s)
			}
		}
	}
	if len(slots) == 0 {
		slots = []string{"—"}
	}
	return strings.Join(plats, ", "), strings.Join(slots, ", ")
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

func location(prof *model.Profile) *time.Location {
	name := "America/Los_Angeles"
	if prof != nil && prof.Timezone != "" {
		name = prof.Timezone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func loadStaticsIndex(jobDir, id string) *StaticsIndex {
	b, err := os.ReadFile(filepath.Join(jobDir, "statics", id, "index.json"))
	if err != nil {
		return nil
	}
	var ix StaticsIndex
	if json.Unmarshal(b, &ix) != nil {
		return nil
	}
	return &ix
}

// WriteBrief renders and writes review/brief.md, records the artifact and
// advances clipped -> drafted. Re-running on a drafted (or later) job only
// rewrites the file.
func WriteBrief(job *model.Job, prof *model.Profile, d *Draft, now time.Time) (string, error) {
	if !job.AtLeast(model.StateClipped) {
		return "", fmt.Errorf("job %s is %s; brief render needs at least clipped", job.ID, job.State)
	}
	md := Render(job, prof, d, now)
	if err := model.WriteFileAtomic(job.Path(BriefFile), []byte(md), 0o644); err != nil {
		return "", err
	}
	job.Artifacts.Brief = BriefFile
	job.Artifacts.Draft = DraftFile
	if job.State == model.StateClipped {
		return md, job.Advance(model.StateDrafted, "brief render", "")
	}
	return md, job.Save()
}
