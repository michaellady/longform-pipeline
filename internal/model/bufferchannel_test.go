package model

import "testing"

func TestBufferChannelExactThenFamily(t *testing.T) {
	p := &Profile{Buffer: BufferConfig{Channels: map[string]string{"instagram": "buf-ig", "linkedin": "buf-li", "linkedin_page": "buf-li-page"}}}
	for plat, want := range map[string]string{
		"instagram":        "buf-ig",      // exact
		"linkedin_page":    "buf-li-page", // exact wins over the family
		"linkedin_profile": "buf-li",      // family
		"facebook":         "",            // neither
		"facebook_page":    "",            // family not set either
	} {
		if got := p.BufferChannel(plat); got != want {
			t.Errorf("BufferChannel(%q) = %q, want %q", plat, got, want)
		}
	}
	var nilProfile *Profile
	if got := nilProfile.BufferChannel("instagram"); got != "" {
		t.Errorf("nil profile: %q", got)
	}
}
