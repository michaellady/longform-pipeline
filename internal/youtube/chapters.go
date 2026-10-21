// Package youtube uploads a finished video to YouTube through the Data API v3:
// OAuth (installed app, loopback redirect, local token file), metadata
// construction including chapter lines, resumable media upload, and
// thumbnail set. It is pure transport; what the title and description say is
// decided upstream.
package youtube

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Chapter is one entry of a --chapters-file. YouTube reads chapters from
// timestamp lines in the description, so this is rendered as text, not sent
// as structured data.
type Chapter struct {
	AtMS  int64  `json:"at_ms"`
	Title string `json:"title"`
}

// YouTube's rules for chapters to render (support.google.com/youtube/answer/9884579):
// the first timestamp is 0:00, there are at least three, they ascend, and each
// chapter is at least ten seconds long. Violations do not fail the upload; the
// chapters just silently do not appear, so we refuse them up front.
const (
	minChapters        = 3
	minChapterLengthMS = 10_000
)

// ParseChaptersFile reads and validates a JSON array of chapters.
func ParseChaptersFile(path string) ([]Chapter, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("chapters file: %w", err)
	}
	ch, err := ParseChapters(b)
	if err != nil {
		return nil, fmt.Errorf("chapters file %s: %w", path, err)
	}
	return ch, nil
}

// ParseChapters decodes and validates a JSON array of chapters.
func ParseChapters(b []byte) ([]Chapter, error) {
	var ch []Chapter
	if err := json.Unmarshal(b, &ch); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if err := ValidateChapters(ch); err != nil {
		return nil, err
	}
	return ch, nil
}

// ValidateChapters enforces YouTube's rendering rules (see minChapters).
func ValidateChapters(ch []Chapter) error {
	if len(ch) < minChapters {
		return fmt.Errorf("need at least %d chapters for YouTube to render them, got %d", minChapters, len(ch))
	}
	if ch[0].AtMS != 0 {
		return fmt.Errorf("first chapter must start at 0 ms, got %d", ch[0].AtMS)
	}
	for i, c := range ch {
		if strings.TrimSpace(c.Title) == "" {
			return fmt.Errorf("chapter %d has an empty title", i)
		}
		if strings.ContainsAny(c.Title, "\r\n") {
			return fmt.Errorf("chapter %d title contains a newline", i)
		}
		if c.AtMS < 0 {
			return fmt.Errorf("chapter %d has a negative timestamp", i)
		}
		if i == 0 {
			continue
		}
		gap := c.AtMS - ch[i-1].AtMS
		if gap <= 0 {
			return fmt.Errorf("chapter %d (%d ms) does not come after chapter %d (%d ms)", i, c.AtMS, i-1, ch[i-1].AtMS)
		}
		if gap < minChapterLengthMS {
			return fmt.Errorf("chapter %d is only %d ms long; YouTube needs at least %d ms", i-1, gap, minChapterLengthMS)
		}
	}
	return nil
}

// FormatTimestamp renders milliseconds as m:ss under an hour and h:mm:ss at
// or above it, the forms YouTube parses from a description.
func FormatTimestamp(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	total := ms / 1000
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// ChapterLines renders chapters as one "m:ss Title" line each.
func ChapterLines(ch []Chapter) string {
	var sb strings.Builder
	for i, c := range ch {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(FormatTimestamp(c.AtMS))
		sb.WriteByte(' ')
		sb.WriteString(strings.TrimSpace(c.Title))
	}
	return sb.String()
}

// AppendChapters adds the chapter block after the description, separated by a
// blank line. An empty chapter list returns the description unchanged.
func AppendChapters(description string, ch []Chapter) string {
	if len(ch) == 0 {
		return description
	}
	desc := strings.TrimRight(description, "\r\n\t ")
	if desc == "" {
		return ChapterLines(ch)
	}
	return desc + "\n\n" + ChapterLines(ch)
}
