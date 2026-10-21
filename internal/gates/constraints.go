package gates

// Vendored from claude-social-media-skills/_shared/publish-constraints/
// constraints.go (D24). Platform keys are renamed to the longform cells
// schema (youtube_shorts -> youtube, facebook_page -> facebook,
// instagram_business -> instagram, tiktok_business -> tiktok,
// linkedin_personal -> linkedin_profile); x, bluesky and pinterest rows are
// added from the Buffer policy caps in _shared/buffer-post-prep/main.go.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// constraints.json is the single source of truth for PLATFORM-NATIVE publish
// limits, per field. It is embedded so the binary is self-contained, and it is
// a committed data file so other tools can read the same ceilings without a
// Go import.
//
// "Native ceiling" matters: a downstream router may impose a TIGHTER policy
// cap (Buffer caps Facebook posts at 500 even though Facebook itself allows
// far more). Those policy caps live in the router and must stay <= these
// ceilings; they are not represented here.
//
//go:embed constraints.json
var constraintsJSON []byte

// FieldConstraint is the limit for one named field (title, description,
// hashtags).
type FieldConstraint struct {
	MaxRunes int `json:"max_runes,omitempty"`
	// MaxCount caps list fields (hashtags). 0 = no cap.
	MaxCount int `json:"max_count,omitempty"`
	// Required: the post fails if this field is empty. Catches the symmetric
	// bug: a YouTube post whose caption went into `description` while `title`
	// was left blank.
	Required bool `json:"required,omitempty"`
	// ForbiddenRe: optional regexp of disallowed characters/sequences. Empty
	// for every platform today; the field exists so a real rejection becomes
	// one data edit, not a code change.
	ForbiddenRe string `json:"forbidden_re,omitempty"`
}

// PlatformConstraints declares, per platform, the cap on each field AND which
// field the human-authored post-text/caption maps to. The whole 2026-05-31
// YouTube bug is the single fact `youtube.title_source == "separate"`:
// post-text goes to `description`, and `title` is a SEPARATE, 100-char-capped
// field, not the caption. On most other platforms the post-text IS the title.
type PlatformConstraints struct {
	PostTextField string                     `json:"post_text_field"`
	TitleSource   string                     `json:"title_source"` // "separate" | "post_text"
	Fields        map[string]FieldConstraint `json:"fields"`
}

// Constraints is the embedded table keyed by cells-schema platform.
var Constraints = mustLoadConstraints()

func mustLoadConstraints() map[string]PlatformConstraints {
	m := map[string]PlatformConstraints{}
	if err := json.Unmarshal(constraintsJSON, &m); err != nil {
		panic(fmt.Sprintf("gates: bad embedded constraints.json: %v", err))
	}
	return m
}

// runeLen counts CHARACTERS, not bytes: platform limits are in characters and
// the voice is full of em-dashes (3 bytes) and emoji (4 bytes).
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// LabelToPlatform maps an OpusClip schedule label (the human
// "<PLATFORM> <account>" string on each scheduled post) to the cells-schema
// platform key. Vendored from publish-constraints/constraints.go; the
// brand-specific "LINKEDIN Mike Lady" -> personal rule is generalised: a
// LINKEDIN label resolves to linkedin_page unless the cell names its
// platform. Pinned to testdata/label-platform.golden.json.
func LabelToPlatform(label string) string {
	switch {
	case strings.HasPrefix(label, "YOUTUBE"):
		return "youtube"
	case strings.HasPrefix(label, "FACEBOOK_PAGE"):
		return "facebook"
	case strings.HasPrefix(label, "INSTAGRAM_BUSINESS"):
		return "instagram"
	case strings.HasPrefix(label, "LINKEDIN_PROFILE"):
		return "linkedin_profile"
	case strings.HasPrefix(label, "LINKEDIN"):
		return "linkedin_page"
	case strings.HasPrefix(label, "TIKTOK_BUSINESS"):
		return "tiktok"
	case strings.HasPrefix(label, "THREADS"):
		return "threads"
	case strings.HasPrefix(label, "X "), strings.HasPrefix(label, "TWITTER"):
		return "x"
	case strings.HasPrefix(label, "BLUESKY"):
		return "bluesky"
	case strings.HasPrefix(label, "PINTEREST"):
		return "pinterest"
	default:
		return "unknown"
	}
}

// PlatformOf resolves the platform a cell targets: the explicit field, else
// the label mapping.
func PlatformOf(c Cell) string {
	if c.Platform != "" {
		return c.Platform
	}
	return LabelToPlatform(c.Label)
}
