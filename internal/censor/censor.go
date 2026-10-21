// Package censor matches transcript words against the strong-profanity list
// (D10), pads and merges the hits into spans, and builds the ffmpeg
// filtergraph that beeps or mutes them. Everything except list discovery is
// pure so it is testable without media.
package censor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/michaellady/longform-pipeline/internal/media"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/transcribe"
)

// List is plugins/longform-pipeline/censor/default.json.
type List struct {
	SchemaVersion string   `json:"schema_version"`
	Tier          string   `json:"tier,omitempty"`
	Notes         string   `json:"notes,omitempty"`
	Words         []string `json:"words"`
}

// LoadList reads a list file.
func LoadList(path string) (*List, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l List
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("censor list %s: %w", path, err)
	}
	if len(l.Words) == 0 {
		return nil, fmt.Errorf("censor list %s: no words", path)
	}
	return &l, nil
}

// ErrNoList is returned when no default list can be located.
var ErrNoList = errors.New("censor list not found")

// FindDefaultList resolves the default list: LONGFORM_CENSOR_LIST, then
// <ConfigRoot>/censor/default.json, then <exe dir>/../censor/default.json
// (the plugin layout: scripts/longform beside censor/), then a walk up from
// cwd for plugins/longform-pipeline/censor/default.json (dev checkouts).
func FindDefaultList() (string, error) {
	var tried []string
	try := func(p string) (string, bool) {
		if p == "" {
			return "", false
		}
		tried = append(tried, p)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
		return "", false
	}
	if p, ok := try(os.Getenv("LONGFORM_CENSOR_LIST")); ok {
		return p, nil
	}
	if p, ok := try(filepath.Join(model.ConfigRoot(), "censor", "default.json")); ok {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		if p, ok := try(filepath.Join(filepath.Dir(exe), "..", "censor", "default.json")); ok {
			return p, nil
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		for dir := cwd; ; dir = filepath.Dir(dir) {
			if p, ok := try(filepath.Join(dir, "plugins", "longform-pipeline", "censor", "default.json")); ok {
				return p, nil
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return "", fmt.Errorf("%w (tried %s)", ErrNoList, strings.Join(tried, ", "))
}

// Span is one censored stretch of audio.
type Span struct {
	Word    string `json:"word"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
}

// Censored is analysis/censored.json.
type Censored struct {
	SchemaVersion string `json:"schema_version"`
	Mode          string `json:"mode"` // "beep" | "mute"
	Spans         []Span `json:"spans"`
}

// DefaultPadMs is added on both sides of every matched word.
const DefaultPadMs = 40

// Vocabulary builds the lookup set from the list and the profile extras,
// cleaned the same way transcript words are.
func Vocabulary(list []string, extra []string) map[string]bool {
	v := map[string]bool{}
	for _, w := range list {
		if c := transcribe.CleanWord(w); c != "" {
			v[c] = true
		}
	}
	for _, w := range extra {
		if c := transcribe.CleanWord(w); c != "" {
			v[c] = true
		}
	}
	return v
}

// Match returns one padded span per word that is in vocab, clamped to
// [0, durationMs] when durationMs > 0. Spans are in word order, unmerged.
func Match(words []transcribe.Word, vocab map[string]bool, padMs, durationMs int64) []Span {
	var out []Span
	for _, w := range words {
		c := transcribe.CleanWord(w.W)
		if c == "" || !vocab[c] {
			continue
		}
		s := Span{Word: c, StartMs: w.StartMs - padMs, EndMs: w.EndMs + padMs}
		if s.StartMs < 0 {
			s.StartMs = 0
		}
		if durationMs > 0 && s.EndMs > durationMs {
			s.EndMs = durationMs
		}
		if s.EndMs <= s.StartMs {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Merge sorts spans and coalesces any that overlap or touch. The merged Word
// lists the distinct words joined by a space.
func Merge(spans []Span) []Span {
	if len(spans) == 0 {
		return []Span{}
	}
	sorted := make([]Span, len(spans))
	copy(sorted, spans)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].StartMs != sorted[j].StartMs {
			return sorted[i].StartMs < sorted[j].StartMs
		}
		return sorted[i].EndMs < sorted[j].EndMs
	})
	out := []Span{sorted[0]}
	for _, s := range sorted[1:] {
		last := &out[len(out)-1]
		if s.StartMs <= last.EndMs {
			if s.EndMs > last.EndMs {
				last.EndMs = s.EndMs
			}
			if !containsWord(last.Word, s.Word) {
				last.Word += " " + s.Word
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

func containsWord(list, w string) bool {
	for _, x := range strings.Fields(list) {
		if x == w {
			return true
		}
	}
	return false
}

// Beep tone parameters: 1 kHz at -12 dBFS with 30 ms fades. ffmpeg's `sine`
// source emits at 1/8 full scale (-18.06 dBFS, measured with volumedetect on
// ffmpeg 8.0.1), so a linear gain of 2 lands the tone at -12.04 dBFS.
const (
	BeepHz      = 1000
	BeepGain    = "2"
	BeepFadeMs  = 30
	ModeBeep    = "beep"
	ModeMute    = "mute"
	OutputLabel = "aout"
)

// FilterGraph builds the single -filter_complex graph: the original audio
// with every span muted, plus (in beep mode) one padded sine branch per span,
// summed by amix with normalize=0 so levels are untouched. Returns "" when
// there are no spans (caller copies the audio through instead).
func FilterGraph(spans []Span, mode string, af media.AudioFormat) string {
	if len(spans) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[0:a]")
	for _, s := range spans {
		fmt.Fprintf(&b, "volume=enable='between(t,%s,%s)':volume=0,", media.FormatSeconds(s.StartMs), media.FormatSeconds(s.EndMs))
	}
	if mode != ModeBeep {
		b.WriteString("anull[" + OutputLabel + "]")
		return b.String()
	}
	b.WriteString(media.AudioFormatFilter(af))
	b.WriteString("[m]")
	for i, s := range spans {
		dur := s.EndMs - s.StartMs
		fade := int64(BeepFadeMs)
		if dur < 2*fade {
			fade = dur / 2
		}
		fmt.Fprintf(&b, ";sine=frequency=%d:sample_rate=%d:duration=%s,volume=%s,afade=t=in:st=0:d=%s,afade=t=out:st=%s:d=%s,%s,adelay=delays=%d:all=1,%s[b%d]",
			BeepHz, af.SampleRate, media.FormatSeconds(dur), BeepGain,
			media.FormatSeconds(fade), media.FormatSeconds(dur-fade), media.FormatSeconds(fade),
			media.PanDuplicateFilter(af), s.StartMs, media.AudioFormatFilter(af), i)
	}
	b.WriteString(";[m]")
	for i := range spans {
		fmt.Fprintf(&b, "[b%d]", i)
	}
	fmt.Fprintf(&b, "amix=inputs=%d:duration=first:normalize=0[%s]", len(spans)+1, OutputLabel)
	return b.String()
}

// Write saves censored.json atomically (always with a non-nil spans array).
func Write(path string, c *Censored) error {
	if c.Spans == nil {
		c.Spans = []Span{}
	}
	if c.SchemaVersion == "" {
		c.SchemaVersion = "1"
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return model.WriteFileAtomic(path, append(b, '\n'), 0o644)
}

// Signature is a stable string over the spans and mode, used by prepare to
// decide whether delivery.mp4 must be re-rendered.
func Signature(c *Censored) string {
	var b strings.Builder
	b.WriteString(c.Mode)
	for _, s := range c.Spans {
		fmt.Fprintf(&b, ";%d-%d", s.StartMs, s.EndMs)
	}
	return b.String()
}
