// Package spotify is the transport side of publishing the full video as a
// Spotify for Creators video-podcast episode. Spotify has no public upload
// API for creators, so the upload itself is done by the publish skill in a
// browser; this package only prepares what the skill uploads: a 16:9 H.264
// episode file (pillarboxed over a blurred fill when the source is vertical)
// and the episode notes in plain and HTML form.
package spotify

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Spotify for Creators episode limits (probed in the wizard 2026-11-09).
const (
	MaxTitleRunes       = 200
	MaxDescriptionRunes = 4000
)

var (
	utmSourceRe   = regexp.MustCompile(`utm_source=[^&#\s"'<>)\]]*`)
	opusTagLineRe = regexp.MustCompile(`^\s*\[opus:[^\]]*\]\s*$`)
	opusTagTailRe = regexp.MustCompile(`\s*\[opus:[^\]]*\]\s*$`)
	blankRunRe    = regexp.MustCompile(`\n{3,}`)
	urlRe         = regexp.MustCompile(`https?://[^\s<>"]+`)
	paraSepRe     = regexp.MustCompile(`\n[ \t]*\n`)
)

// Notes turns the full video's YouTube description into Spotify episode
// notes: every utm_source value becomes utmSource (default "spotify"), the
// trailing [opus:<id>] provenance tag is removed, lines made only of
// hashtags are removed, runs of blank lines collapse to one and the result
// is trimmed. Timestamp lines ("00:00 Intro") are kept: Spotify turns them
// into chapters.
func Notes(description, utmSource string) string {
	if utmSource == "" {
		utmSource = "spotify"
	}
	s := strings.ReplaceAll(description, "\r\n", "\n")
	s = utmSourceRe.ReplaceAllStringFunc(s, func(m string) string {
		// Sentence punctuation after the value is not part of it.
		trail := m[len("utm_source="):]
		end := len(trail)
		for end > 0 && strings.ContainsRune(".,;:!?", rune(trail[end-1])) {
			end--
		}
		return "utm_source=" + utmSource + trail[end:]
	})
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if opusTagLineRe.MatchString(line) || hashtagOnly(line) {
			continue
		}
		line = opusTagTailRe.ReplaceAllString(line, "")
		out = append(out, strings.TrimRight(line, " \t"))
	}
	s = strings.Join(out, "\n")
	s = blankRunRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// hashtagOnly reports whether a line holds one or more hashtags and nothing
// else.
func hashtagOnly(line string) bool {
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	for _, w := range f {
		if len(w) < 2 || w[0] != '#' {
			return false
		}
	}
	return true
}

// NotesHTML renders plain notes for Spotify's HTML description mode:
// paragraphs (split on blank lines) become <p>, single newlines <br>, URLs
// links, and &, <, > and quotes are escaped.
func NotesHTML(notes string) string {
	notes = strings.TrimSpace(strings.ReplaceAll(notes, "\r\n", "\n"))
	if notes == "" {
		return ""
	}
	var paras []string
	for _, p := range paraSepRe.Split(notes, -1) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		lines := strings.Split(p, "\n")
		for i, l := range lines {
			lines[i] = linkify(strings.TrimSpace(l))
		}
		paras = append(paras, "<p>"+strings.Join(lines, "<br>")+"</p>")
	}
	return strings.Join(paras, "\n")
}

// linkify escapes a line and wraps each URL in an anchor. Trailing
// sentence punctuation stays outside the link.
func linkify(line string) string {
	var b strings.Builder
	last := 0
	for _, m := range urlRe.FindAllStringIndex(line, -1) {
		start, end := m[0], m[1]
		for end > start && strings.ContainsRune(".,;:!?)]'", rune(line[end-1])) {
			end--
		}
		b.WriteString(html.EscapeString(line[last:start]))
		u := html.EscapeString(line[start:end])
		b.WriteString(`<a href="` + u + `">` + u + `</a>`)
		last = end
	}
	b.WriteString(html.EscapeString(line[last:]))
	return b.String()
}

// RuneLen counts characters, the unit Spotify's limits are in.
func RuneLen(s string) int { return utf8.RuneCountInString(s) }

// CheckLimits returns a problem per field over Spotify's limits ("" title
// is a problem too: the wizard requires one).
func CheckLimits(title, notes string) []string {
	var out []string
	switch n := RuneLen(strings.TrimSpace(title)); {
	case n == 0:
		out = append(out, "title is empty")
	case n > MaxTitleRunes:
		out = append(out, "title is "+strconv.Itoa(n)+" characters, over Spotify's "+strconv.Itoa(MaxTitleRunes))
	}
	if n := RuneLen(notes); n > MaxDescriptionRunes {
		out = append(out, "notes are "+strconv.Itoa(n)+" characters, over Spotify's "+strconv.Itoa(MaxDescriptionRunes))
	}
	return out
}
