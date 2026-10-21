// Package slots is the cadence arithmetic (D12, D13): it assigns publish_at
// to every unscheduled cell from the profile's local slot times and
// per-platform daily cadence, counting what is already live, and parks
// anything past the backlog horizon as held. Pure arithmetic, no judgment.
//
// Generalised from claude-social-media-skills/opus-clips/schedule_clips.py
// (5/day at fixed local slot times, roll to the next day) to one cadence per
// platform with a live-queue offset.
package slots

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/gates"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// FullSlotLocal is the wall-clock slot the full video takes (D13).
const FullSlotLocal = "09:00"

// MinLeadTime is how far after approval the full video may go.
const MinLeadTime = 2 * time.Hour

// Options tunes a run. Zero ApprovedAt means now; zero Now means time.Now().
type Options struct {
	From       string    // YYYY-MM-DD; earliest local day the full video may take
	ApprovedAt time.Time // when the human approved; the full video goes >= 2h later
	Now        time.Time
}

// Summary is what `slots propose` prints to stderr.
type Summary struct {
	Full     int    `json:"full"`
	Clips    int    `json:"clips"`
	Statics  int    `json:"statics"`
	Held     int    `json:"held"`
	Skipped  int    `json:"skipped"` // platform not connected in the profile
	Kept     int    `json:"kept"`    // cells that already had publish_at
	FirstDay string `json:"first_day,omitempty"`
	LastDay  string `json:"last_day,omitempty"`
	FullDay  string `json:"full_day,omitempty"`
}

func (s Summary) String() string {
	return fmt.Sprintf("slots: full=%d clips=%d statics=%d held=%d skipped=%d kept=%d full_day=%s range=%s..%s",
		s.Full, s.Clips, s.Statics, s.Held, s.Skipped, s.Kept, s.FullDay, s.FirstDay, s.LastDay)
}

type slotTime struct{ h, m int }

func parseSlots(in []string) ([]slotTime, error) {
	var out []slotTime
	for _, s := range in {
		hh, mm, ok := strings.Cut(strings.TrimSpace(s), ":")
		if !ok {
			return nil, fmt.Errorf("slot %q is not HH:MM", s)
		}
		h, err1 := strconv.Atoi(hh)
		m, err2 := strconv.Atoi(mm)
		if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return nil, fmt.Errorf("slot %q is not HH:MM", s)
		}
		out = append(out, slotTime{h, m})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("profile slots_local is empty")
	}
	return out, nil
}

func (s slotTime) key() string { return fmt.Sprintf("%02d:%02d", s.h, s.m) }

// day is a local calendar day; arithmetic goes through time.Date so DST
// transitions never shift the wall-clock slot.
type day struct {
	y    int
	m    time.Month
	d    int
	loc  *time.Location
	zero time.Time
}

func dayOf(t time.Time, loc *time.Location) day {
	lt := t.In(loc)
	return day{lt.Year(), lt.Month(), lt.Day(), loc, time.Time{}}
}

func (d day) add(n int) day {
	t := time.Date(d.y, d.m, d.d+n, 12, 0, 0, 0, d.loc)
	return day{t.Year(), t.Month(), t.Day(), d.loc, time.Time{}}
}

func (d day) at(s slotTime) time.Time {
	return time.Date(d.y, d.m, d.d, s.h, s.m, 0, 0, d.loc)
}

func (d day) key() string { return fmt.Sprintf("%04d-%02d-%02d", d.y, d.m, d.d) }

// ledger counts what is already placed per (platform/sub-account, local
// day). Facebook and LinkedIn share one postAccountId across pages, so the
// bucket is gates.CadenceKey, not the platform alone.
type ledger struct {
	cadence map[string]int             // bucket|day -> clip+full posts
	statics map[string]int             // bucket|day -> static posts
	used    map[string]map[string]bool // bucket|day -> HH:MM taken
}

func newLedger() *ledger {
	return &ledger{cadence: map[string]int{}, statics: map[string]int{}, used: map[string]map[string]bool{}}
}

func lkey(bucket, day string) string { return bucket + "|" + day }

func (l *ledger) take(bucket, day, slot string, kind string) {
	k := lkey(bucket, day)
	if kind == gates.KindStatic {
		l.statics[k]++
	} else {
		l.cadence[k]++
	}
	if l.used[k] == nil {
		l.used[k] = map[string]bool{}
	}
	l.used[k][slot] = true
}

func (l *ledger) record(c gates.Cell, loc *time.Location) {
	if c.Parked() || c.PublishAt == "" {
		return
	}
	t, err := c.PublishTime()
	if err != nil {
		return
	}
	l.take(gates.CadenceKey(c), gates.LocalDay(t, loc), gates.LocalSlot(t, loc), c.Kind)
}

func (l *ledger) freeSlot(bucket, day string, slots []slotTime) (slotTime, bool) {
	used := l.used[lkey(bucket, day)]
	for _, s := range slots {
		if !used[s.key()] {
			return s, true
		}
	}
	return slotTime{}, false
}

// Propose fills publish_at on every cell whose publish_at is empty and
// returns the full array in input order. Cells already scheduled (and every
// cell in existing) count toward cadence. Timezone comes from the profile.
func Propose(p *model.Profile, cells, existing []gates.Cell, opt Options) ([]gates.Cell, Summary, error) {
	var sum Summary
	if p == nil {
		return nil, sum, fmt.Errorf("slots: profile is required")
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return nil, sum, fmt.Errorf("slots: profile timezone %q: %w", p.Timezone, err)
	}
	slotTimes, err := parseSlots(p.SlotsLocal)
	if err != nil {
		return nil, sum, fmt.Errorf("slots: %w", err)
	}
	fullSlot, _ := parseSlots([]string{FullSlotLocal})
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	approved := opt.ApprovedAt
	if approved.IsZero() {
		approved = now
	}
	horizon := p.BacklogDays
	if horizon <= 0 {
		horizon = 25
	}

	// Anchor: the next 09:00 local at least MinLeadTime after approval (D13),
	// not before --from.
	anchor := dayOf(approved, loc)
	for anchor.at(fullSlot[0]).Sub(approved) < MinLeadTime {
		anchor = anchor.add(1)
	}
	if opt.From != "" {
		ft, err := time.ParseInLocation("2006-01-02", opt.From, loc)
		if err != nil {
			return nil, sum, fmt.Errorf("slots: --from %q: %w", opt.From, err)
		}
		if from := dayOf(ft, loc); from.at(fullSlot[0]).After(anchor.at(fullSlot[0])) {
			anchor = from
		}
	}
	clipStart := anchor.add(1)

	led := newLedger()
	for _, c := range existing {
		led.record(c, loc)
	}
	out := make([]gates.Cell, len(cells))
	copy(out, cells)
	for _, c := range out {
		if c.PublishAt != "" && !c.Parked() {
			led.record(c, loc)
			sum.Kept++
		}
	}

	days := map[string]bool{}
	place := func(i int, d day, s slotTime) {
		t := d.at(s)
		out[i].PublishAt = t.UTC().Format(time.RFC3339)
		out[i].Status = ""
		led.take(gates.CadenceKey(out[i]), d.key(), s.key(), out[i].Kind)
		days[d.key()] = true
	}
	hold := func(i int) {
		out[i].PublishAt = ""
		out[i].Status = gates.StatusHeld
		sum.Held++
	}
	// skip parks a cell whose platform has no connected account in the
	// profile (probe: OpusClip had no TikTok or X account). Reports true
	// when the cell was skipped.
	skip := func(i int) bool {
		if gates.AccountFor(p, gates.PlatformOf(out[i]), out[i].Kind) != "" {
			return false
		}
		out[i].PublishAt = ""
		out[i].Status = gates.StatusSkipped
		sum.Skipped++
		return true
	}

	// Pass 1: full cells at the anchor slot, rolling forward only when the
	// platform's cadence is already spent on that day.
	for i, c := range out {
		if c.Kind != gates.KindFull || c.PublishAt != "" || skip(i) {
			continue
		}
		bucket := gates.CadenceKey(c)
		cap := gates.CadenceFor(p, gates.PlatformOf(c))
		placed := false
		for off := 0; off < horizon && cap > 0; off++ {
			d := anchor.add(off)
			if led.cadence[lkey(bucket, d.key())] >= cap {
				continue
			}
			if led.used[lkey(bucket, d.key())][fullSlot[0].key()] {
				continue
			}
			place(i, d, fullSlot[0])
			sum.Full++
			placed = true
			break
		}
		if !placed {
			hold(i)
		}
	}
	sum.FullDay = anchor.key()

	// Pass 2: clips from the day after the full video, filling slots in
	// order, at most Cadence[platform] per local day, across the backlog.
	for i, c := range out {
		if c.Kind != gates.KindClip || c.PublishAt != "" || skip(i) {
			continue
		}
		bucket := gates.CadenceKey(c)
		cap := gates.CadenceFor(p, gates.PlatformOf(c))
		placed := false
		for off := 0; off < horizon && cap > 0; off++ {
			d := clipStart.add(off)
			if led.cadence[lkey(bucket, d.key())] >= cap {
				continue
			}
			s, ok := led.freeSlot(bucket, d.key(), slotTimes)
			if !ok {
				continue
			}
			place(i, d, s)
			sum.Clips++
			placed = true
			break
		}
		if !placed {
			hold(i)
		}
	}

	// Pass 3: statics from day two, one per platform per day, in a slot the
	// platform is not already using that day.
	for i, c := range out {
		if c.Kind != gates.KindStatic || c.PublishAt != "" || skip(i) {
			continue
		}
		bucket := gates.CadenceKey(c)
		placed := false
		for off := 0; off < horizon; off++ {
			d := clipStart.add(off)
			if led.statics[lkey(bucket, d.key())] >= 1 {
				continue
			}
			s, ok := led.freeSlot(bucket, d.key(), slotTimes)
			if !ok {
				continue
			}
			place(i, d, s)
			sum.Statics++
			placed = true
			break
		}
		if !placed {
			hold(i)
		}
	}

	if len(days) > 0 {
		keys := make([]string, 0, len(days))
		for k := range days {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sum.FirstDay, sum.LastDay = keys[0], keys[len(keys)-1]
	}
	return out, sum, nil
}
