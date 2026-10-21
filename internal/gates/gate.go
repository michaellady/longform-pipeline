package gates

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// Verdict is one refusal. One JSON object per line on stdout, exit 65.
type Verdict struct {
	ClipID   string `json:"clip_id"`
	Label    string `json:"label,omitempty"`
	Platform string `json:"platform"`
	Field    string `json:"field"`
	Reason   string `json:"reason"`
	FixHint  string `json:"fix_hint,omitempty"`
}

// Options tunes a validation run. Zero values mean: now = time.Now(),
// no censor list, profile defaults for everything else.
type Options struct {
	Now    time.Time
	Censor *Censor
}

// Validate runs every check over cells. Existing cells (the live OpusClip
// schedule plus the Buffer queue) only feed the cadence count; they are never
// judged themselves. The returned slice is empty when every cell passes.
func Validate(p *model.Profile, cells, existing []Cell, opt Options) ([]Verdict, error) {
	if p == nil {
		return nil, fmt.Errorf("gate: profile is required")
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return nil, fmt.Errorf("gate: profile timezone %q: %w", p.Timezone, err)
	}
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	maxDur := MaxDurations(p)

	var out []Verdict
	for _, c := range cells {
		out = append(out, validateCaps(c)...)
		out = append(out, validateAccount(p, c)...)
		out = append(out, validateDuration(c, maxDur)...)
		out = append(out, validateCensor(c, opt.Censor)...)
		out = append(out, validateTiming(c, now)...)
	}
	out = append(out, validateCadence(p, cells, existing, loc)...)
	return out, nil
}

// validateCaps is the vendored publish-constraints check (a): per-platform,
// per-field caps and the separate-title rule, plus hashtag count and the
// caption-plus-hashtags budget.
func validateCaps(c Cell) []Verdict {
	platform := PlatformOf(c)
	pc, ok := Constraints[platform]
	if !ok {
		// Unknown platform: a new connected account no row covers. FAIL
		// CLOSED: never let an unvalidated cell through.
		return []Verdict{{
			ClipID: c.ClipID, Label: c.Label, Platform: platform, Field: "platform",
			Reason:  fmt.Sprintf("no constraints row for platform %q (label %q)", platform, c.Label),
			FixHint: "add a row to internal/gates/constraints.json and the golden fixture",
		}}
	}
	fieldVal := map[string]string{"title": c.Title, "description": c.Description}
	names := make([]string, 0, len(pc.Fields))
	for n := range pc.Fields {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic verdict order: description, hashtags, title

	var out []Verdict
	for _, h := range c.Hashtags {
		if !hashtagRe.MatchString(h) {
			out = append(out, Verdict{c.ClipID, c.Label, platform, "hashtags",
				fmt.Sprintf("hashtag %q is malformed", h),
				"hashtags are one word of letters, digits or _ with an optional leading #"})
			break
		}
	}
	for _, name := range names {
		fc := pc.Fields[name]
		if name == "hashtags" {
			out = append(out, validateHashtags(c, platform, fc)...)
			continue
		}
		val := fieldVal[name]
		if fc.Required && strings.TrimSpace(val) == "" {
			fh := ""
			if pc.TitleSource == "separate" && name == "title" {
				fh = fmt.Sprintf("%s needs a SEPARATE %q field; post-text goes to %q, not %q",
					platform, "title", pc.PostTextField, "title")
			}
			out = append(out, Verdict{c.ClipID, c.Label, platform, name, "required field is empty", fh})
			continue
		}
		if fc.MaxRunes > 0 {
			if n := runeLen(val); n > fc.MaxRunes {
				fh := fmt.Sprintf("trim %q to <= %d chars", name, fc.MaxRunes)
				if pc.TitleSource == "separate" && name == "title" {
					fh = fmt.Sprintf("%s maps post-text to %q; supply a separate <=%d-char title, don't reuse the caption",
						platform, pc.PostTextField, fc.MaxRunes)
				}
				out = append(out, Verdict{c.ClipID, c.Label, platform, name,
					fmt.Sprintf("%s length %d exceeds %s cap %d", name, n, platform, fc.MaxRunes), fh})
			}
		}
		if fc.ForbiddenRe != "" {
			re, err := regexp.Compile(fc.ForbiddenRe)
			if err == nil && re.MatchString(val) {
				out = append(out, Verdict{c.ClipID, c.Label, platform, name,
					fmt.Sprintf("%s contains forbidden characters (/%s/)", name, fc.ForbiddenRe),
					"remove the disallowed characters"})
			}
		}
	}
	// Hashtags are appended to the post text on every platform, so the
	// budget that matters is caption + " #tag" * n, even when each field
	// passes alone.
	if ptf := pc.Fields[pc.PostTextField]; ptf.MaxRunes > 0 && len(c.Hashtags) > 0 {
		base := fieldVal[pc.PostTextField]
		total := runeLen(base)
		for _, h := range c.Hashtags {
			if strings.Contains(base, "#"+strings.TrimPrefix(h, "#")) {
				continue // already inline
			}
			total += 1 + runeLen("#"+strings.TrimPrefix(h, "#"))
		}
		if total > ptf.MaxRunes {
			out = append(out, Verdict{c.ClipID, c.Label, platform, "hashtags",
				fmt.Sprintf("%s plus hashtags length %d exceeds %s cap %d", pc.PostTextField, total, platform, ptf.MaxRunes),
				fmt.Sprintf("drop hashtags or trim %q so the combined text fits in %d chars", pc.PostTextField, ptf.MaxRunes)})
		}
	}
	return out
}

var hashtagRe = regexp.MustCompile(`^#?[\p{L}\p{N}_]+$`)

func validateHashtags(c Cell, platform string, fc FieldConstraint) []Verdict {
	if fc.MaxCount > 0 && len(c.Hashtags) > fc.MaxCount {
		return []Verdict{{c.ClipID, c.Label, platform, "hashtags",
			fmt.Sprintf("hashtag count %d exceeds %s cap %d", len(c.Hashtags), platform, fc.MaxCount),
			fmt.Sprintf("keep at most %d hashtags", fc.MaxCount)}}
	}
	return nil
}

// validateAccount refuses a cell whose platform is not connected in the
// profile (probe: OpusClip had no TikTok or X account) or whose platform
// shares one postAccountId across pages and names no sub-account.
func validateAccount(p *model.Profile, c Cell) []Verdict {
	platform := PlatformOf(c)
	if _, known := Constraints[platform]; !known {
		return nil // already failed closed in validateCaps
	}
	var out []Verdict
	want := AccountFor(p, platform, c.Kind)
	switch {
	case want == "":
		out = append(out, Verdict{c.ClipID, c.Label, platform, "account_id",
			fmt.Sprintf("profile has no account for %s %s cells", platform, c.Kind),
			"connect the account in OpusClip and add it to the profile"})
	case c.AccountID == "":
		out = append(out, Verdict{c.ClipID, c.Label, platform, "account_id",
			"account_id is empty",
			"connect the account in OpusClip and add it to the profile"})
	case c.AccountID != want:
		out = append(out, Verdict{c.ClipID, c.Label, platform, "account_id",
			fmt.Sprintf("account_id %q is not the profile's %s account %q", c.AccountID, platform, want),
			"connect the account in OpusClip and add it to the profile"})
	}
	if SubAccountRequired[platform] && c.Kind != KindStatic && c.SubAccountID == "" {
		out = append(out, Verdict{c.ClipID, c.Label, platform, "sub_account_id",
			fmt.Sprintf("%s shares one postAccountId across pages; sub_account_id is empty", platform),
			"set sub_account_id to the page or profile id from the profile's opusclip.sub_accounts"})
	}
	return out
}

// validateDuration is check (b): clip and full cells must fit the platform's
// maximum. Statics carry no video.
func validateDuration(c Cell, maxDur map[string]float64) []Verdict {
	if c.Kind != KindClip && c.Kind != KindFull {
		return nil
	}
	platform := PlatformOf(c)
	limit, ok := maxDur[platform]
	if !ok || c.DurationSec <= limit {
		return nil
	}
	return []Verdict{{c.ClipID, c.Label, platform, "duration_sec",
		fmt.Sprintf("duration %.0fs exceeds %s max %.0fs", c.DurationSec, platform, limit),
		"pick a shorter clip for this platform or drop the cell"}}
}

// validateCensor is check (c): no listed word in title, description or
// hashtags (D10).
func validateCensor(c Cell, cz *Censor) []Verdict {
	if cz == nil || cz.Len() == 0 {
		return nil
	}
	platform := PlatformOf(c)
	var out []Verdict
	if w := cz.Match(c.Title); w != "" {
		out = append(out, censorVerdict(c, platform, "title", w))
	}
	if w := cz.Match(c.Description); w != "" {
		out = append(out, censorVerdict(c, platform, "description", w))
	}
	for _, h := range c.Hashtags {
		if w := cz.Match(strings.TrimPrefix(h, "#")); w != "" {
			out = append(out, censorVerdict(c, platform, "hashtags", w))
			break
		}
	}
	return out
}

func censorVerdict(c Cell, platform, field, word string) Verdict {
	return Verdict{c.ClipID, c.Label, platform, field,
		fmt.Sprintf("%s contains listed word %q", field, word),
		"rewrite without the word; the list is censor/default.json plus profile censor.extra"}
}

// validateTiming is check (e): every non-held cell has a parseable
// publish_at in the future.
func validateTiming(c Cell, now time.Time) []Verdict {
	if c.Parked() {
		return nil
	}
	platform := PlatformOf(c)
	t, err := c.PublishTime()
	if err != nil {
		return []Verdict{{c.ClipID, c.Label, platform, "publish_at",
			err.Error(), "run `longform slots propose` first; publish_at is RFC3339 UTC"}}
	}
	if !t.After(now) {
		return []Verdict{{c.ClipID, c.Label, platform, "publish_at",
			fmt.Sprintf("publish_at %s is not after now %s", t.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)),
			"re-run `longform slots propose` to move the cell into the future"}}
	}
	return nil
}

// validateCadence is check (d): cells plus existing never exceed
// Cadence[platform] on any local day, counted per (platform, sub-account).
// Each offending cell from the batch gets its own verdict; existing cells
// are counted, never judged.
func validateCadence(p *model.Profile, cells, existing []Cell, loc *time.Location) []Verdict {
	type key struct{ bucket, platform, day string }
	count := map[key]int{}
	add := func(c Cell) key {
		if !c.CountsCadence() {
			return key{}
		}
		t, err := c.PublishTime()
		if err != nil {
			return key{}
		}
		k := key{CadenceKey(c), PlatformOf(c), LocalDay(t, loc)}
		count[k]++
		return k
	}
	for _, c := range existing {
		add(c)
	}
	keys := make([]key, len(cells))
	for i, c := range cells {
		keys[i] = add(c)
	}
	var out []Verdict
	for i, c := range cells {
		k := keys[i]
		if k.platform == "" {
			continue
		}
		cap := CadenceFor(p, k.platform)
		if n := count[k]; n > cap {
			out = append(out, Verdict{c.ClipID, c.Label, k.platform, "publish_at",
				fmt.Sprintf("%d %s posts on %s exceed cadence %d", n, k.bucket, k.day, cap),
				"re-run `longform slots propose` with the live schedule in --existing"})
		}
	}
	return out
}
