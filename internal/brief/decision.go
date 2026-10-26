package brief

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// DecisionFile and ScheduleFile are the job-relative outputs of parse-decision.
const (
	DecisionFile = "review/decision.json"
	ScheduleFile = "review/schedule.json"
)

// ErrGrammar marks a grammar the parser could not accept (exit 65).
var ErrGrammar = errors.New("approval grammar")

// Decision is review/decision.json.
type DecisionRecord struct {
	SchemaVersion string            `json:"schema_version"`
	Grammar       string            `json:"grammar"`
	Approved      bool              `json:"approved"`
	HeldClips     []int             `json:"held_clips"`     // brief table row numbers
	DroppedClips  []int             `json:"dropped_clips"`  // brief table row numbers
	HeldPlatforms []string          `json:"held_platforms"` // as typed; family names cover sub-platforms
	Thumbnail     int               `json:"thumbnail"`      // 1-based chosen thumbnail, 0 = none
	Decisions     map[string]string `json:"decisions"`
	Note          string            `json:"note,omitempty"` // reject reason
	At            time.Time         `json:"at"`
}

// ScheduleCell is one row of review/schedule.json, the shape the gates read.
type ScheduleCell struct {
	Kind         string   `json:"kind"` // clip | full | static
	ClipID       string   `json:"clip_id"`
	ProjectID    string   `json:"project_id"`
	Platform     string   `json:"platform"`
	AccountID    string   `json:"account_id"`
	SubAccountID string   `json:"sub_account_id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Hashtags     []string `json:"hashtags"`
	DurationSec  float64  `json:"duration_sec"`
	PublishAt    string   `json:"publish_at"`
	Label        string   `json:"label"`
}

// Schedule is review/schedule.json.
type Schedule struct {
	SchemaVersion string         `json:"schema_version"`
	JobID         string         `json:"job_id"`
	Cells         []ScheduleCell `json:"cells"`
}

// ParseGrammar turns the reply into a DecisionRecord. Commands are separated
// by `;` or `,`; inside `approve except` and `drop`, commas join row numbers.
// `reject` takes everything after it as the note, punctuation included, so it
// is always the last command. Any token that is not a command is an error
// naming it.
func ParseGrammar(grammar string, d *Draft) (*DecisionRecord, error) {
	toks, ends := tokenize(grammar)
	if len(toks) == 0 {
		return nil, fmt.Errorf("%w: empty", ErrGrammar)
	}
	rec := &DecisionRecord{SchemaVersion: "1", Grammar: grammar, Approved: true, HeldClips: []int{}, DroppedClips: []int{}, HeldPlatforms: []string{}, Decisions: map[string]string{}, Thumbnail: d.ChosenThumbnail()}
	rows := len(d.Clips)
	i := 0
	next := func() (string, bool) {
		if i < len(toks) {
			return toks[i], true
		}
		return "", false
	}
	isSep := func(t string) bool { return t == ";" || t == "," }
	// readRows parses n[,n...] starting at toks[i].
	readRows := func(cmd string) ([]int, error) {
		var out []int
		for {
			t, ok := next()
			if !ok || isSep(t) {
				if len(out) == 0 {
					return nil, fmt.Errorf("%w: %s needs at least one clip row number", ErrGrammar, cmd)
				}
				return out, nil
			}
			n, err := strconv.Atoi(t)
			if err != nil {
				return nil, fmt.Errorf("%w: unknown token %q after %s (expected a clip row number)", ErrGrammar, t, cmd)
			}
			if n < 1 || n > rows {
				return nil, fmt.Errorf("%w: clip row %d is out of range 1..%d", ErrGrammar, n, rows)
			}
			out = append(out, n)
			i++
			// A comma followed by a number continues the list; otherwise the
			// comma separates commands.
			if t2, ok := next(); ok && t2 == "," && i+1 < len(toks) {
				if _, err := strconv.Atoi(toks[i+1]); err == nil {
					i++
					continue
				}
			}
			return out, nil
		}
	}
	// readText collects tokens until a separator.
	readText := func() string {
		var parts []string
		for {
			t, ok := next()
			if !ok || t == ";" || t == "," {
				break
			}
			parts = append(parts, t)
			i++
		}
		return strings.Join(parts, " ")
	}

	for i < len(toks) {
		t := toks[i]
		if isSep(t) {
			i++
			continue
		}
		i++
		switch strings.ToLower(t) {
		case "approve":
			if t2, ok := next(); ok && strings.EqualFold(t2, "except") {
				i++
				ns, err := readRows("approve except")
				if err != nil {
					return nil, err
				}
				rec.HeldClips = appendUnique(rec.HeldClips, ns...)
			}
		case "hold":
			p, ok := next()
			if !ok || isSep(p) {
				return nil, fmt.Errorf("%w: hold needs a platform", ErrGrammar)
			}
			i++
			p = strings.ToLower(p)
			matched := false
			for _, known := range d.Platforms() {
				if PlatformMatches(p, known) {
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("%w: unknown platform %q (draft has %s)", ErrGrammar, p, strings.Join(d.Platforms(), ", "))
			}
			rec.HeldPlatforms = appendUniqueStr(rec.HeldPlatforms, p)
		case "drop":
			ns, err := readRows("drop")
			if err != nil {
				return nil, err
			}
			rec.DroppedClips = appendUnique(rec.DroppedClips, ns...)
		case "swap":
			if t2, ok := next(); !ok || !strings.EqualFold(t2, "thumbnail") {
				return nil, fmt.Errorf("%w: unknown token %q after swap (expected thumbnail)", ErrGrammar, t2)
			}
			i++
			t3, ok := next()
			if !ok || isSep(t3) {
				return nil, fmt.Errorf("%w: swap thumbnail needs a number", ErrGrammar)
			}
			n, err := strconv.Atoi(t3)
			if err != nil {
				return nil, fmt.Errorf("%w: unknown token %q after swap thumbnail (expected a number)", ErrGrammar, t3)
			}
			if n < 1 || n > len(d.FullVideo.Thumbnails) {
				return nil, fmt.Errorf("%w: thumbnail %d is out of range 1..%d", ErrGrammar, n, len(d.FullVideo.Thumbnails))
			}
			i++
			rec.Thumbnail = n
		case "decide":
			id, ok := next()
			if !ok || isSep(id) {
				return nil, fmt.Errorf("%w: decide needs a decision id", ErrGrammar)
			}
			i++
			var dec *Decision
			for k := range d.Decisions {
				if strings.EqualFold(d.Decisions[k].ID, id) {
					dec = &d.Decisions[k]
				}
			}
			if dec == nil {
				return nil, fmt.Errorf("%w: unknown decision id %q", ErrGrammar, id)
			}
			text := readText()
			if text == "" {
				return nil, fmt.Errorf("%w: decide %s needs an option", ErrGrammar, dec.ID)
			}
			choice, err := matchOption(dec, text)
			if err != nil {
				return nil, err
			}
			rec.Decisions[dec.ID] = choice
		case "reject", "reject:":
			// A reason is prose and may contain commas or semicolons, so the
			// note is the raw remainder of the text, not the next tokens.
			rec.Approved = false
			note := strings.TrimSpace(grammar[ends[i-1]:])
			rec.Note = strings.TrimSpace(strings.TrimPrefix(note, ":"))
			i = len(toks)
		default:
			return nil, fmt.Errorf("%w: unknown token %q", ErrGrammar, t)
		}
	}
	// Decisions left unanswered take their default.
	for _, dec := range d.Decisions {
		if _, ok := rec.Decisions[dec.ID]; !ok && dec.Default != "" {
			rec.Decisions[dec.ID] = dec.Default
		}
	}
	sort.Ints(rec.HeldClips)
	sort.Ints(rec.DroppedClips)
	return rec, nil
}

func matchOption(dec *Decision, text string) (string, error) {
	if len(dec.Options) == 0 {
		return text, nil
	}
	if n, err := strconv.Atoi(text); err == nil && n >= 1 && n <= len(dec.Options) {
		return dec.Options[n-1], nil
	}
	for _, o := range dec.Options {
		if strings.EqualFold(strings.TrimSpace(o), strings.TrimSpace(text)) {
			return o, nil
		}
	}
	return "", fmt.Errorf("%w: decide %s: %q is not one of: %s", ErrGrammar, dec.ID, text, strings.Join(dec.Options, " | "))
}

// tokenize splits on whitespace and makes `;` and `,` their own tokens.
// tokenize splits the grammar into words and the separators `;` and `,`, and
// records the byte offset just past each token so a command can take the raw
// remainder of the text.
func tokenize(s string) (toks []string, ends []int) {
	start := -1
	flush := func(end int) {
		if start >= 0 {
			toks = append(toks, s[start:end])
			ends = append(ends, end)
			start = -1
		}
	}
	for i, r := range s {
		switch {
		case r == ';' || r == ',':
			flush(i)
			toks = append(toks, string(r))
			ends = append(ends, i+1)
		case unicode.IsSpace(r):
			flush(i)
		default:
			if start < 0 {
				start = i
			}
		}
	}
	flush(len(s))
	return toks, ends
}

func appendUnique(xs []int, ns ...int) []int {
	for _, n := range ns {
		dup := false
		for _, x := range xs {
			if x == n {
				dup = true
			}
		}
		if !dup {
			xs = append(xs, n)
		}
	}
	return xs
}

func appendUniqueStr(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

// DeriveSchedule builds the surviving cells. Clip rows are brief table rows
// (OrderedClips order). Full-video cells take account ids from the profile
// and the next first-slot time after now (D13); static cells go to Buffer's
// queue and carry no publish_at.
func DeriveSchedule(job *model.Job, prof *model.Profile, d *Draft, rec *DecisionRecord, now time.Time) *Schedule {
	sch := &Schedule{SchemaVersion: "1", JobID: job.ID, Cells: []ScheduleCell{}}
	if !rec.Approved {
		return sch
	}
	held := func(platform string) bool {
		for _, p := range rec.HeldPlatforms {
			if PlatformMatches(p, platform) {
				return true
			}
		}
		return false
	}
	inRows := func(rows []int, row int) bool {
		for _, r := range rows {
			if r == row {
				return true
			}
		}
		return false
	}

	// Full video first: it gets the earliest slot.
	var fullDur float64
	if job.Delivery != nil {
		fullDur = float64(job.Delivery.DurationMs) / 1000
	}
	fullAt := nextFirstSlot(prof, now)
	for _, p := range d.FullVideo.Platforms {
		if held(p) {
			continue
		}
		acct, sub := "", ""
		if prof != nil {
			acct = prof.OpusClip.Accounts[p]
			sub = prof.OpusClip.SubAccounts[p]
		}
		sch.Cells = append(sch.Cells, ScheduleCell{Kind: "full", ClipID: d.FullVideo.ClipID, ProjectID: d.FullVideo.ProjectID, Platform: p, AccountID: acct, SubAccountID: sub, Title: d.FullVideo.Title, Description: d.FullVideo.Description, Hashtags: []string{}, DurationSec: fullDur, PublishAt: fullAt, Label: "full video"})
	}

	for row0, idx := range d.OrderedClips() {
		row := row0 + 1
		c := d.Clips[idx]
		if c.Status != "post" || inRows(rec.HeldClips, row) || inRows(rec.DroppedClips, row) {
			continue
		}
		for _, cell := range c.Cells {
			if held(cell.Platform) {
				continue
			}
			tags := cell.Hashtags
			if tags == nil {
				tags = []string{}
			}
			sch.Cells = append(sch.Cells, ScheduleCell{Kind: "clip", ClipID: c.ClipID, ProjectID: c.ProjectID, Platform: cell.Platform, AccountID: cell.AccountID, SubAccountID: cell.SubAccountID, Title: cell.Title, Description: cell.Description, Hashtags: tags, DurationSec: c.DurationSec, PublishAt: cell.PublishAt, Label: fmt.Sprintf("clip %d: %s", row, c.ShortTitle)})
		}
	}

	for _, s := range d.Statics {
		for _, p := range s.Platforms {
			if held(p) {
				continue
			}
			acct := prof.BufferChannel(p) // exact key, else family (linkedin for linkedin_page)
			// The caption is the post text: title, like every non-YouTube
			// clip cell (the gate requires a title on every platform), and
			// description too, where YouTube and Pinterest read post text.
			sch.Cells = append(sch.Cells, ScheduleCell{Kind: "static", ClipID: s.ID, ProjectID: "", Platform: p, AccountID: acct, Title: s.Caption, Description: s.Caption, Hashtags: []string{}, DurationSec: 0, PublishAt: "", Label: fmt.Sprintf("static %s (%s)", s.ID, s.Format)})
		}
	}
	return sch
}

// nextFirstSlot is the next occurrence of the profile's first local slot
// strictly after now, as RFC3339 in UTC.
func nextFirstSlot(prof *model.Profile, now time.Time) string {
	loc := location(prof)
	slot := "09:00"
	if prof != nil && len(prof.SlotsLocal) > 0 {
		slot = prof.SlotsLocal[0]
	}
	hh, mm := 9, 0
	fmt.Sscanf(slot, "%d:%d", &hh, &mm)
	local := now.In(loc)
	t := time.Date(local.Year(), local.Month(), local.Day(), hh, mm, 0, 0, loc)
	if !t.After(local) {
		t = t.AddDate(0, 0, 1)
	}
	return t.UTC().Format(time.RFC3339)
}

// ApplyDecision writes decision.json and schedule.json and moves the job:
// drafted -> approved on approve, back to drafted (with the note) on reject.
func ApplyDecision(job *model.Job, prof *model.Profile, d *Draft, rec *DecisionRecord, now time.Time) (*Schedule, error) {
	if !job.AtLeast(model.StateDrafted) {
		return nil, fmt.Errorf("job %s is %s; parse-decision needs drafted", job.ID, job.State)
	}
	rec.At = now.UTC()
	sch := DeriveSchedule(job, prof, d, rec, now)
	if err := writeJSON(job.Path(DecisionFile), rec); err != nil {
		return nil, err
	}
	if err := writeJSON(job.Path(ScheduleFile), sch); err != nil {
		return nil, err
	}
	job.Artifacts.Decision = DecisionFile
	job.Artifacts.Schedule = ScheduleFile
	if !rec.Approved {
		if job.State != model.StateDrafted {
			job.History = append(job.History, model.Transition{From: job.State, To: model.StateDrafted, At: now.UTC(), By: "brief parse-decision", Note: "rejected: " + rec.Note})
			job.State = model.StateDrafted
		} else {
			job.History = append(job.History, model.Transition{From: job.State, To: job.State, At: now.UTC(), By: "brief parse-decision", Note: "rejected: " + rec.Note})
		}
		return sch, job.Save()
	}
	if job.State == model.StateDrafted {
		return sch, job.Advance(model.StateApproved, "brief parse-decision", "")
	}
	return sch, job.Save()
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return model.WriteFileAtomic(path, append(b, '\n'), 0o644)
}
