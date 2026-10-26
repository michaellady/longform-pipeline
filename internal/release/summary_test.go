package release

import "testing"

func TestExtractSummary(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"until next heading": {
			"## Summary\nFirst.\nSecond.\n\n## Verified\nTests.", "First.\nSecond."},
		"CRLF and deeper heading": {
			"## Summary\r\nOne.\r\n\r\n### Test plan\r\n- x\r\n", "One."},
		"last section drops the attribution footer": {
			"## Summary\nOnly this.\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)\n", "Only this."},
		"co-authored-by line dropped": {
			"## Summary\nText.\nCo-Authored-By: Someone <x@y>\n", "Text."},
		"hash line inside a code fence is not a heading": {
			"## Summary\nRun:\n```bash\n# comment\nmake install\n```\nDone.\n## Next\nno", "Run:\n```bash\n# comment\nmake install\n```\nDone."},
		"any heading level, any case": {
			"intro\n# SUMMARY\nShort.\n# Other", "Short."},
		"preceding sections ignored": {
			"## Why\nreasons\n## Summary\nThe point.\n", "The point."},
		"no summary heading": {
			"Fixes the cadence check.\n\n## Details\nx", ""},
		"summary word in text is not a heading": {
			"Summary: none here", ""},
		"empty summary section": {
			"## Summary\n\n## Verified\nx", ""},
		"empty body": {"", ""},
	} {
		if got := ExtractSummary(tc.body); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestComposeTitle(t *testing.T) {
	for _, tc := range []struct {
		entries []entry
		want    string
	}{
		{nil, ""},
		{[]entry{{Number: 1, Title: "a"}}, "a"},
		{[]entry{{Number: 1, Title: "a"}, {Number: 2, Title: "b"}}, "b, and 1 more change"},
		{[]entry{{Title: "a"}, {Title: "b"}, {Number: 3, Title: "c"}}, "c, and 2 more changes"},
	} {
		if got := composeTitle(tc.entries); got != tc.want {
			t.Errorf("composeTitle(%v) = %q, want %q", tc.entries, got, tc.want)
		}
	}
}

func TestComposeSummary(t *testing.T) {
	for name, tc := range map[string]struct {
		entries []entry
		want    string
	}{
		"none":                {nil, Fallback},
		"one with summary":    {[]entry{{Number: 1, Title: "t", Summary: "s"}}, "s"},
		"one without summary": {[]entry{{Number: 1, Title: "t"}}, "t\n\n" + Fallback},
		"several, all summarised": {[]entry{{Number: 1, Title: "a", Summary: "sa"}, {Number: 2, Title: "b", Summary: "sb"}},
			"**a (#1)**\n\nsa\n\n**b (#2)**\n\nsb"},
		"several, one bare commit": {[]entry{{Title: "direct push"}, {Number: 2, Title: "b", Summary: "sb"}},
			"**direct push**\n\n**b (#2)**\n\nsb\n\n" + Fallback},
	} {
		if got := composeSummary(tc.entries); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
