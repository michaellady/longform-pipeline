// Package transcribe runs whisper.cpp (whisper-cli) and normalises its full
// JSON into the pipeline's transcript schema (docs/transcript-schema.md).
//
// Invocation: `whisper-cli -m <model> -f <wav> -l <lang> -ojf -ml 1 -sow -np
// -of <base>`. With --max-len 1 and --split-on-word whisper.cpp emits one
// segment per word, each with millisecond offsets, which is the only mode in
// whisper.cpp 1.9.1 that yields reliable per-word start/end without DTW
// heuristics. Sentence segments are rebuilt from the words (punctuation or a
// gap of more than one second ends a segment).
package transcribe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// Word is one spoken word with millisecond bounds.
type Word struct {
	W       string `json:"w"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
}

// Segment is a sentence-ish run of words.
type Segment struct {
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

// Transcript is analysis/transcript.json.
type Transcript struct {
	SchemaVersion string    `json:"schema_version"`
	Source        string    `json:"source"` // "whisper.cpp" | "skipped"
	Model         string    `json:"model"`  // model file basename
	Language      string    `json:"language"`
	DurationMs    int64     `json:"duration_ms"`
	Words         []Word    `json:"words"`
	Segments      []Segment `json:"segments"`
}

// DefaultModelName is the model prepare looks for under <ConfigRoot>/models.
const DefaultModelName = "ggml-medium.en-q5_0.bin"

// ModelPath resolves the whisper model: profile value, then
// LONGFORM_WHISPER_MODEL, then <ConfigRoot>/models/ggml-medium.en-q5_0.bin.
func ModelPath(profileModel string) string {
	if profileModel != "" {
		return model.ExpandHome(profileModel)
	}
	if v := os.Getenv("LONGFORM_WHISPER_MODEL"); v != "" {
		return model.ExpandHome(v)
	}
	return filepath.Join(model.ConfigRoot(), "models", DefaultModelName)
}

// FindCLI locates whisper-cli on PATH or in the Homebrew prefix.
func FindCLI() (string, error) {
	if p, err := exec.LookPath("whisper-cli"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/whisper-cli", "/usr/local/bin/whisper-cli"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("whisper-cli not found on PATH or in /opt/homebrew/bin")
}

// Options configures a whisper run.
type Options struct {
	CLI      string // path to whisper-cli
	Model    string // path to the ggml model
	Language string // "en"
	Threads  int    // 0 = whisper default
	Log      io.Writer
}

// Args returns the whisper-cli argument vector (exposed for tests).
func Args(o Options, wav, outBase string) []string {
	args := []string{"-m", o.Model, "-f", wav}
	if o.Language != "" {
		args = append(args, "-l", o.Language)
	}
	if o.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(o.Threads))
	}
	return append(args, "-ojf", "-ml", "1", "-sow", "-np", "-of", outBase)
}

// Run transcribes wav and writes <outBase>.json (whisper's full JSON).
func Run(o Options, dir, wav, outBase string) error {
	if o.CLI == "" {
		return errors.New("transcribe: no whisper-cli")
	}
	if st, err := os.Stat(o.Model); err != nil || st.IsDir() {
		return fmt.Errorf("transcribe: whisper model not found at %s (set profile.transcribe.model or LONGFORM_WHISPER_MODEL)", o.Model)
	}
	args := Args(o, wav, outBase)
	cmd := exec.Command(o.CLI, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if o.Log != nil {
		fmt.Fprintf(o.Log, "$ %s %s\n", o.CLI, strings.Join(args, " "))
	}
	err := cmd.Run()
	if o.Log != nil && stderr.Len() > 0 {
		o.Log.Write(stderr.Bytes())
	}
	if err != nil {
		return fmt.Errorf("transcribe: whisper-cli: %v", err)
	}
	return nil
}

// whisperFull is the subset of `-ojf` output we read.
type whisperFull struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []struct {
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Text string `json:"text"`
	} `json:"transcription"`
}

// Normalize converts whisper's full JSON into a Transcript. modelPath is
// recorded by basename; durationMs comes from the media probe.
func Normalize(raw []byte, modelPath, language string, durationMs int64) (*Transcript, error) {
	var wf whisperFull
	if err := json.Unmarshal(raw, &wf); err != nil {
		return nil, fmt.Errorf("whisper json: %w", err)
	}
	t := &Transcript{SchemaVersion: "1", Source: "whisper.cpp", Model: filepath.Base(modelPath), Language: language, DurationMs: durationMs, Words: []Word{}, Segments: []Segment{}}
	if wf.Result.Language != "" {
		t.Language = wf.Result.Language
	}
	for _, seg := range wf.Transcription {
		text := strings.TrimSpace(seg.Text)
		if text == "" || isMarker(text) {
			continue
		}
		fields := strings.Fields(text)
		var keep []string
		for _, f := range fields {
			if !isMarker(f) {
				keep = append(keep, f)
			}
		}
		if len(keep) == 0 {
			continue
		}
		start, end := seg.Offsets.From, seg.Offsets.To
		if end < start {
			end = start
		}
		if len(keep) == 1 {
			t.Words = append(t.Words, Word{W: keep[0], StartMs: start, EndMs: end})
			continue
		}
		// A multi-word segment (rare under -ml 1): split time by character share.
		total := 0
		for _, w := range keep {
			total += len(w)
		}
		cursor := start
		for i, w := range keep {
			var wEnd int64
			if i == len(keep)-1 {
				wEnd = end
			} else {
				wEnd = cursor + (end-start)*int64(len(w))/int64(total)
			}
			t.Words = append(t.Words, Word{W: w, StartMs: cursor, EndMs: wEnd})
			cursor = wEnd
		}
	}
	t.Segments = BuildSegments(t.Words)
	return t, nil
}

// isMarker filters whisper's non-speech tokens: [_BEG_], [_TT_123],
// [BLANK_AUDIO], (music), *laughs*.
func isMarker(s string) bool {
	if len(s) < 2 {
		return false
	}
	switch {
	case s[0] == '[' && s[len(s)-1] == ']',
		s[0] == '(' && s[len(s)-1] == ')',
		s[0] == '*' && s[len(s)-1] == '*':
		return true
	}
	return false
}

// SegmentGapMs ends a segment when the next word starts this long after the
// previous one ended.
const SegmentGapMs = 1000

// BuildSegments groups words into sentences on terminal punctuation or a gap.
func BuildSegments(words []Word) []Segment {
	segs := []Segment{}
	var cur *Segment
	var buf []string
	flush := func() {
		if cur != nil {
			cur.Text = strings.Join(buf, " ")
			segs = append(segs, *cur)
		}
		cur, buf = nil, nil
	}
	for i, w := range words {
		if cur != nil && w.StartMs-cur.EndMs > SegmentGapMs {
			flush()
		}
		if cur == nil {
			cur = &Segment{StartMs: w.StartMs}
		}
		buf = append(buf, w.W)
		cur.EndMs = w.EndMs
		if endsSentence(w.W) || i == len(words)-1 {
			flush()
		}
	}
	flush()
	return segs
}

func endsSentence(w string) bool {
	r := []rune(strings.TrimRightFunc(w, func(r rune) bool { return r == '"' || r == '\'' || r == ')' }))
	if len(r) == 0 {
		return false
	}
	switch r[len(r)-1] {
	case '.', '?', '!':
		return true
	}
	return false
}

// Empty returns the transcript written when transcription is skipped.
func Empty(language string, durationMs int64) *Transcript {
	return &Transcript{SchemaVersion: "1", Source: "skipped", Model: "", Language: language, DurationMs: durationMs, Words: []Word{}, Segments: []Segment{}}
}

// Write saves a transcript atomically.
func Write(path string, t *Transcript) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return model.WriteFileAtomic(path, append(b, '\n'), 0o644)
}

// Load reads a transcript.
func Load(path string) (*Transcript, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Transcript
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &t, nil
}

// CleanWord lowercases and strips every non-letter/digit rune; the censor list
// is matched on this form so "F*cking," never matters and "Fucking," does.
func CleanWord(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
