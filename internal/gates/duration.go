package gates

import (
	"encoding/json"
	"strconv"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// DefaultMaxDurationSec is the per-platform ceiling for clip and full-video
// cells (plan section 4.7). Platforms absent here (bluesky, pinterest) have
// no duration check until a probe sets Profile.Extra["max_duration_sec"].
var DefaultMaxDurationSec = map[string]float64{
	"threads":          300,
	"linkedin_page":    600,
	"linkedin_profile": 600,
	"instagram":        900,
	"tiktok":           600,
	"facebook":         14400,
	"youtube":          43200,
	"x":                140,
}

// MaxDurations returns the default table overlaid with
// Profile.Extra["max_duration_sec"] (a platform -> seconds map; numbers or
// numeric strings). Unparseable overrides are ignored.
func MaxDurations(p *model.Profile) map[string]float64 {
	out := make(map[string]float64, len(DefaultMaxDurationSec))
	for k, v := range DefaultMaxDurationSec {
		out[k] = v
	}
	if p == nil || p.Extra == nil {
		return out
	}
	raw, ok := p.Extra["max_duration_sec"].(map[string]any)
	if !ok {
		return out
	}
	for k, v := range raw {
		if f, ok := toFloat(v); ok && f > 0 {
			out[k] = f
		}
	}
	return out
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

// AccountFor is the profile's account id for a cell's transport: OpusClip's
// postAccountId for clips and the full video, the Buffer channel id for
// statics. Empty means the platform is not connected (probe: OpusClip had no
// TikTok or X account), so nothing may be placed or fired there.
func AccountFor(p *model.Profile, platform, kind string) string {
	if p == nil {
		return ""
	}
	if kind == KindStatic {
		return p.Buffer.Channels[platform]
	}
	return p.OpusClip.Accounts[platform]
}

// SubAccountRequired lists platforms where one postAccountId spans several
// pages or profiles (probe: three Facebook pages; LinkedIn org page plus
// person profile), so a cell must name its sub_account_id and cadence is
// counted per (platform, sub_account_id).
var SubAccountRequired = map[string]bool{"facebook": true, "linkedin_page": true, "linkedin_profile": true}

// CadenceKey is the bucket cadence is counted in: platform plus
// sub-account, so two Facebook pages each get their own daily allowance.
func CadenceKey(c Cell) string {
	k := PlatformOf(c)
	if c.SubAccountID != "" {
		k += "/" + c.SubAccountID
	}
	return k
}

// CadenceFor resolves the daily cadence (D12) for a platform: the exact key,
// else the family key before the first underscore (linkedin_page ->
// linkedin), else 0 meaning "never post here".
func CadenceFor(p *model.Profile, platform string) int {
	if p == nil || p.Cadence == nil {
		return 0
	}
	if v, ok := p.Cadence[platform]; ok {
		return v
	}
	for i := 0; i < len(platform); i++ {
		if platform[i] == '_' {
			if v, ok := p.Cadence[platform[:i]]; ok {
				return v
			}
			break
		}
	}
	return 0
}
