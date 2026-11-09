// Package newsletter reads the shared voice corpus and produces the
// canonical newsletter CTA, so longform image posts end with exactly the
// string the newsletter skills use. Pure transport: the title is the newest
// newsletter in the corpus, the CTA text comes from _shared/cta.sh.
package newsletter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrNoNewsletter means the corpus holds no post with source_type newsletter.
var ErrNoNewsletter = errors.New("voice corpus has no newsletter post")

// Post is one corpus entry (claude-social-media-skills/_shared/voice-corpus).
type Post struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	PublishedAt string `json:"published_at"`
	SourceType  string `json:"source_type"`
}

// Latest returns the newest post with source_type "newsletter" in the
// corpus file. published_at is a date (2006-01-02) or RFC 3339; an
// unparseable date sorts oldest. Ties keep the first in file order.
func Latest(corpusPath string) (Post, error) {
	b, err := os.ReadFile(corpusPath)
	if err != nil {
		return Post{}, err
	}
	var c struct {
		Posts []Post `json:"posts"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Post{}, fmt.Errorf("%s: %w", corpusPath, err)
	}
	var best Post
	var bestAt time.Time
	found := false
	for _, p := range c.Posts {
		if p.SourceType != "newsletter" || strings.TrimSpace(p.Title) == "" {
			continue
		}
		at := parseDate(p.PublishedAt)
		if !found || at.After(bestAt) {
			best, bestAt, found = p, at, true
		}
	}
	if !found {
		return Post{}, fmt.Errorf("%w: %s", ErrNoNewsletter, corpusPath)
	}
	return best, nil
}

func parseDate(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02", time.RFC1123Z, time.RFC1123} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Time{}
}

// CTA runs the canonical CTA script with the title and returns its output
// exactly (the script prints no trailing newline; one is trimmed if
// present). The text is the Comment-to-DM trigger, so it is never rebuilt
// here.
func CTA(script, title string) (string, error) {
	if script == "" {
		return "", errors.New("no cta script configured (cta.cta_script or statics.carousel_dir)")
	}
	cmd := exec.Command("bash", script, title)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v: %s", script, err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// CanonicalCTA is CTA(script, Latest(corpus).Title).
func CanonicalCTA(corpusPath, script string) (string, Post, error) {
	p, err := Latest(corpusPath)
	if err != nil {
		return "", Post{}, err
	}
	cta, err := CTA(script, p.Title)
	return cta, p, err
}

// EndsWithCTA reports whether caption ends with cta, ignoring trailing
// whitespace only.
func EndsWithCTA(caption, cta string) bool {
	return cta != "" && strings.HasSuffix(strings.TrimRight(caption, " \t\r\n"), cta)
}
