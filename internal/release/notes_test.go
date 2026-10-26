package release

import (
	"strings"
	"testing"
)

func TestNotesShape(t *testing.T) {
	m := Meta{
		Version: "v0.2.2", Previous: "v0.2.1", SHA: "4d0cb6b",
		Summary: "**a (#16)**\n\nFirst.\n\n**b (#17)**\n\nSecond.\n",
		Changes: []string{"docs: a (#16)", "code: b (#17)"},
	}
	sums := "abc  longform_v0.2.2_darwin_amd64.tar.gz\ndef  longform_v0.2.2_darwin_arm64.tar.gz\n"
	got := Notes(m, "michaellady/longform-pipeline", sums)
	want := "## Summary\n\n**a (#16)**\n\nFirst.\n\n**b (#17)**\n\nSecond.\n\n" +
		"<details><summary>Technical details</summary>\n\n" +
		"### Install\n\n```bash\n" +
		"curl -sSL https://github.com/michaellady/longform-pipeline/releases/download/v0.2.2/longform_v0.2.2_darwin_$(uname -m | sed s/x86_64/amd64/).tar.gz | tar -xz\n" +
		"./longform version\n```\n\n" +
		"Or from a checkout: `make install` fetches the latest release for this Mac.\n\n" +
		"### Changes since v0.2.1\n\n- docs: a (#16)\n- code: b (#17)\n\n" +
		"### Checksums (sha256)\n\n```\n" + strings.TrimRight(sums, "\n") + "\n```\n\n" +
		"</details>\n"
	if got != want {
		t.Errorf("notes:\n%s\nwant:\n%s", got, want)
	}
}

func TestNotesFirstReleaseAndRepublish(t *testing.T) {
	got := Notes(Meta{Version: "v0.1.0", Previous: NoPrevious, Summary: Fallback, SHA: "abc"}, "o/r", "x")
	if !strings.Contains(got, "### Changes\n\n- no new commits; this release republishes abc\n") {
		t.Errorf("notes:\n%s", got)
	}
	if !strings.HasPrefix(got, "## Summary\n\n"+Fallback+"\n\n<details>") {
		t.Errorf("summary is not first:\n%s", got)
	}
}
