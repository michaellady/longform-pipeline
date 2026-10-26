package release

import (
	"fmt"
	"strings"
)

// Notes renders the GitHub release body: "## Summary" first for a
// non-technical reader (AGENTS.md rule 12), then everything technical
// collapsed under <details>: install, the commits since the previous
// version, and the sha256 checksums of the assets.
func Notes(m Meta, repo, checksums string) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("## Summary\n\n%s\n\n", strings.TrimSpace(m.Summary))
	w("<details><summary>Technical details</summary>\n\n")

	w("### Install\n\n")
	w("```bash\n")
	w("curl -sSL https://github.com/%s/releases/download/%s/longform_%s_darwin_$(uname -m | sed s/x86_64/amd64/).tar.gz | tar -xz\n", repo, m.Version, m.Version)
	w("./longform version\n")
	w("```\n\n")
	w("Or from a checkout: `make install` fetches the latest release for this Mac.\n\n")

	if m.Previous == NoPrevious {
		w("### Changes\n\n")
	} else {
		w("### Changes since %s\n\n", m.Previous)
	}
	if len(m.Changes) == 0 {
		w("- no new commits; this release republishes %s\n", m.SHA)
	}
	for _, c := range m.Changes {
		w("- %s\n", c)
	}
	w("\n")

	w("### Checksums (sha256)\n\n")
	w("```\n%s\n```\n\n", strings.TrimRight(checksums, "\n"))
	w("</details>\n")
	return b.String()
}
