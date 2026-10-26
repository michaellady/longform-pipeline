package release

import (
	"fmt"
	"regexp"
	"strings"
)

// Fallback is the release summary used when the merged PR has no "## Summary"
// section: a sentence that says what the tool is, for a reader who has never
// seen the repo (AGENTS.md rule 12). It is the sentence the tag-push release
// workflow used before releases became automatic.
const Fallback = "This release updates the longform-pipeline tool that turns one long-form phone recording into scheduled clips, a full-length upload, and static posts after a single approval. Install or update it with `make install` or the tarball below; nothing changes in your brand profiles or scheduled posts unless the notes under Details say so."

var (
	headingRE = regexp.MustCompile(`^\s{0,3}#{1,6}(\s|$)`)
	summaryRE = regexp.MustCompile(`(?i)^\s{0,3}#{1,6}\s+summary\s*#*\s*$`)
	fenceRE   = regexp.MustCompile("^\\s{0,3}(```|~~~)")
	// Attribution footers agents append to PR bodies; they describe the PR,
	// not the release.
	footerRE = regexp.MustCompile(`(?i)^\s*(🤖\s*generated with|co-authored-by:)`)
)

// ExtractSummary returns the text under the body's "Summary" heading up to
// the next heading, trimmed, or "" when the body has no such heading. A "#"
// line inside a fenced code block is not a heading.
func ExtractSummary(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var (
		in, fenced bool
		out        []string
	)
	for _, line := range strings.Split(body, "\n") {
		if fenceRE.MatchString(line) {
			fenced = !fenced
		}
		if !fenced && headingRE.MatchString(line) {
			if in {
				break
			}
			if summaryRE.MatchString(line) {
				in = true
				continue
			}
		}
		if in && !footerRE.MatchString(line) {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// entry is one merged change in a release: a PR, or a bare commit when the
// commit subject names no PR.
type entry struct {
	Number  int // 0 for a commit with no PR
	Title   string
	Summary string
}

func (e entry) label() string {
	if e.Number == 0 {
		return e.Title
	}
	return fmt.Sprintf("%s (#%d)", e.Title, e.Number)
}

// composeTitle is the newest change's title; when several changes ship
// together it says how many more there are.
func composeTitle(entries []entry) string {
	if len(entries) == 0 {
		return ""
	}
	newest := entries[len(entries)-1].Title
	switch more := len(entries) - 1; more {
	case 0:
		return newest
	case 1:
		return newest + ", and 1 more change"
	default:
		return fmt.Sprintf("%s, and %d more changes", newest, more)
	}
}

// composeSummary builds the release's Summary text. One change: its PR
// summary, or its title followed by Fallback. Several: each change's title
// in bold with its summary underneath, oldest first and newest last, and
// Fallback once at the end when any of them had no summary.
func composeSummary(entries []entry) string {
	switch len(entries) {
	case 0:
		return Fallback
	case 1:
		e := entries[0]
		if e.Summary != "" {
			return e.Summary
		}
		return e.Title + "\n\n" + Fallback
	}
	var (
		parts   []string
		missing bool
	)
	for _, e := range entries {
		p := "**" + e.label() + "**"
		if e.Summary != "" {
			p += "\n\n" + e.Summary
		} else {
			missing = true
		}
		parts = append(parts, p)
	}
	if missing {
		parts = append(parts, Fallback)
	}
	return strings.Join(parts, "\n\n")
}
