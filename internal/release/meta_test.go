package release

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixtureGH is the real GH client with its Runner swapped for one that
// answers `gh pr view N` from testdata/pr-N.json, so every Compute test also
// exercises the JSON gh prints.
func fixtureGH(t *testing.T) (GH, *[][]string) {
	t.Helper()
	var calls [][]string
	run := func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name != "gh" || len(args) < 3 || args[0] != "pr" || args[1] != "view" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		b, err := os.ReadFile(filepath.Join("testdata", "pr-"+args[2]+".json"))
		if err != nil {
			return nil, fmt.Errorf("gh pr view %s: no pull requests found", args[2])
		}
		return b, nil
	}
	return GH{Repo: "michaellady/longform-pipeline", Run: run}, &calls
}

type fakeGit struct {
	head    string
	tags    []string // reachable from head
	other   []string // exist elsewhere, not reachable
	commits []Commit // first-parent commits since the latest reachable tag
	files   []string // changed since the latest reachable tag
	from    string   // the range start Compute asked for
}

func (f *fakeGit) Resolve(context.Context, string) (string, error) { return f.head, nil }
func (f *fakeGit) Tags(context.Context, string) ([]string, error)  { return f.tags, nil }
func (f *fakeGit) ChangedFiles(context.Context, string, string) ([]string, error) {
	return f.files, nil
}
func (f *fakeGit) Commits(_ context.Context, from, _ string) ([]Commit, error) {
	f.from = from
	return f.commits, nil
}
func (f *fakeGit) TagExists(_ context.Context, tag string) (bool, error) {
	for _, t := range append(append([]string{}, f.tags...), f.other...) {
		if t == tag {
			return true, nil
		}
	}
	return false, nil
}

func commits(subjects ...string) []Commit {
	cs := make([]Commit, len(subjects))
	for i, s := range subjects {
		cs[i] = Commit{SHA: fmt.Sprintf("%07x0000000000000000000000000000000000", i+1), Subject: s}
	}
	return cs
}

var released = []string{"v0.1.0", "v0.1.1", "v0.2.0", "v0.2.1"}

func compute(t *testing.T, g *fakeGit, opt Options) Meta {
	t.Helper()
	gh, _ := fixtureGH(t)
	m, err := Compute(context.Background(), g, gh, opt)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	return m
}

func TestPRNumber(t *testing.T) {
	for subject, want := range map[string]int{
		"opus schedule: re-runnable (#19)":              19,
		"lifecycle: seal (#18)  ":                       18,
		"Merge pull request #7 from michaellady/feat/x": 7,
		"fix: handle (#3) in the middle of a subject":   0,
		"shortcut: mark workflow uses Save to Photos":   0,
		"Revert \"gate: cadence (#12)\" (#13)":          13,
	} {
		if got := PRNumber(subject); got != want {
			t.Errorf("PRNumber(%q) = %d, want %d", subject, got, want)
		}
	}
}

func TestDocsOnly(t *testing.T) {
	for path, want := range map[string]bool{
		"docs/install.md":       true,
		"docs/probes/opus.json": true,
		"README.md":             true,
		"plugins/longform-pipeline/skills/x/SKILL.md": true,
		".github/workflows/release.yml":               true,
		"internal/release/meta.go":                    false,
		"Makefile":                                    false,
		"plugins/longform-pipeline/scripts/setup.sh":  false,
		"go.mod":           false,
		"cmd/docs/main.go": false,
		"plugins/longform-pipeline/.claude-plugin/x.json": false,
	} {
		if got := DocsOnly(path); got != want {
			t.Errorf("DocsOnly(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestComputePatchByDefault(t *testing.T) {
	g := &fakeGit{head: "4d0cb6b", tags: released,
		commits: commits("opus schedule: re-runnable; statics pass the gate without hand edits (#19)"),
		files:   []string{"internal/cli/cmd_opus.go", "docs/cli-contract.md"}}
	m := compute(t, g, Options{})
	if g.from != "v0.2.1" {
		t.Errorf("range starts at %q, want v0.2.1", g.from)
	}
	want := Meta{
		Version: "v0.2.2", Previous: "v0.2.1", Bump: Patch, Release: true, SHA: "4d0cb6b",
		Title:   "opus schedule is re-runnable and statics pass the gate",
		Summary: "A crashed or repeated approval phase used to create duplicate OpusClip schedules. It now skips every cell that already has a recorded post.",
		Reason:  "1 of 2 changed files since v0.2.1 are not docs or workflow files; bump patch: no release:minor or release:major label on #19",
		PRs:     []int{19},
		Changes: []string{"opus schedule: re-runnable; statics pass the gate without hand edits (#19)"},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("got  %+v\nwant %+v", m, want)
	}
}

func TestComputeMinorLabel(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("watcher: Photos album feed (#20)"),
		files:   []string{"internal/photos/photos.go"}}, Options{})
	if m.Bump != Minor || m.Version != "v0.3.0" {
		t.Errorf("bump %s version %s, want minor v0.3.0", m.Bump, m.Version)
	}
	if !strings.Contains(m.Reason, "bump minor: release:minor on #20") {
		t.Errorf("reason %q does not name the label", m.Reason)
	}
	// CRLF body from the GitHub web editor; the ### heading ends the section.
	if m.Summary != "The watcher now reads a Photos album.\nNothing else changes." {
		t.Errorf("summary %q", m.Summary)
	}
}

func TestComputeHighestLabelAcrossPRsWins(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("watcher: Photos album feed (#20)", "manifest v2 (#23)", "gate: sixth post (#21)"),
		files:   []string{"internal/manifest/manifest.go"}}, Options{})
	if m.Bump != Major || m.Version != "v1.0.0" {
		t.Errorf("bump %s version %s, want major v1.0.0", m.Bump, m.Version)
	}
	if !strings.Contains(m.Reason, "bump major: release:major on #23") {
		t.Errorf("reason %q", m.Reason)
	}
}

func TestComputeDocsOnlySkips(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("docs: operator guide (#22)"),
		files:   []string{"docs/operator-guide.md", "README.md", ".github/workflows/ci.yml"}}, Options{})
	if m.Release {
		t.Fatal("docs-only merge released")
	}
	if !strings.HasPrefix(m.Reason, "only docs or workflow files changed since v0.2.1 (3 files)") {
		t.Errorf("reason %q", m.Reason)
	}
	if m.Version != "v0.2.2" {
		t.Errorf("version %s: the would-be version is still reported", m.Version)
	}
}

// A docs merge after a skipped code merge releases both: the file check
// covers everything since the previous tag, not just the last PR.
func TestComputeDocsMergeAfterSkippedCodeReleasesBoth(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("refactor: rename internal package (#24)", "docs: operator guide (#22)"),
		files:   []string{"internal/x/x.go", "docs/operator-guide.md"}}, Options{})
	if !m.Release {
		t.Fatalf("not released: %s", m.Reason)
	}
	want := "**refactor: rename internal package (#24)**\n\nInternal rename, nothing a user sees.\n\n" +
		"**docs: operator guide (#22)**\n\nThe operator guide explains the steps a person does by hand."
	if m.Summary != want {
		t.Errorf("summary\n%s\nwant\n%s", m.Summary, want)
	}
	if m.Title != "docs: operator guide, and 1 more change" {
		t.Errorf("title %q", m.Title)
	}
}

func TestComputeSkipLabel(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("refactor: rename internal package (#24)"),
		files:   []string{"internal/x/x.go"}}, Options{})
	if m.Release || !strings.HasPrefix(m.Reason, "release:skip label on #24") {
		t.Errorf("release %v reason %q", m.Release, m.Reason)
	}
}

func TestComputeForceLabelReleasesDocsOnly(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("docs: install guide says releases are automatic (#25)"),
		files:   []string{"docs/install.md"}}, Options{})
	if !m.Release || !strings.HasPrefix(m.Reason, "release:force label on #25") {
		t.Errorf("release %v reason %q", m.Release, m.Reason)
	}
}

func TestComputeManualRunOverridesSkipAndDocsOnly(t *testing.T) {
	for _, subject := range []string{"refactor: rename internal package (#24)", "docs: operator guide (#22)"} {
		m := compute(t, &fakeGit{head: "h", tags: released, commits: commits(subject),
			files: []string{"docs/operator-guide.md"}}, Options{Force: true})
		if !m.Release || !strings.HasPrefix(m.Reason, "manual run") {
			t.Errorf("%s: release %v reason %q", subject, m.Release, m.Reason)
		}
	}
}

func TestComputeBumpOverride(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("manifest v2 (#23)"), files: []string{"go.mod"}},
		Options{Bump: Patch, Force: true})
	if m.Bump != Patch || m.Version != "v0.2.2" || !strings.Contains(m.Reason, "bump patch: requested by the manual run") {
		t.Errorf("bump %s version %s reason %q", m.Bump, m.Version, m.Reason)
	}
}

func TestComputeNoPRNumberBumpsPatchAndSaysSo(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("hotfix pushed without a PR"), files: []string{"Makefile"}}, Options{})
	if m.Bump != Patch || !m.Release {
		t.Fatalf("bump %s release %v", m.Bump, m.Release)
	}
	if !strings.Contains(m.Reason, "bump patch: no PR number found in the merged commit subjects (0000001)") {
		t.Errorf("reason %q", m.Reason)
	}
	if m.Title != "hotfix pushed without a PR" || m.Summary != "hotfix pushed without a PR\n\n"+Fallback {
		t.Errorf("title %q summary %q", m.Title, m.Summary)
	}
	if len(m.PRs) != 0 {
		t.Errorf("prs %v", m.PRs)
	}
}

func TestComputeHeadWithoutPRNumberAmongPRs(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("gate: sixth post (#21)", "rebased commit"), files: []string{"Makefile"}}, Options{})
	if !strings.Contains(m.Reason, "no PR number in the head commit subject") {
		t.Errorf("reason %q", m.Reason)
	}
}

func TestComputeSummaryFallsBackToTitleAndSentence(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("gate: reject a sixth post on one day (#21)"), files: []string{"internal/gates/gates.go"}}, Options{})
	want := "gate: reject a sixth post on one day\n\n" + Fallback
	if m.Summary != want {
		t.Errorf("summary %q", m.Summary)
	}
}

func TestComputeSeveralPRsNewestLast(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("gate: sixth post (#21)", "watcher: Photos album feed (#20)", "opus schedule (#19)"),
		files:   []string{"internal/x.go"}}, Options{})
	want := "**gate: reject a sixth post on one day (#21)**\n\n" +
		"**watcher: Photos album feed (#20)**\n\nThe watcher now reads a Photos album.\nNothing else changes.\n\n" +
		"**opus schedule is re-runnable and statics pass the gate (#19)**\n\nA crashed or repeated approval phase used to create duplicate OpusClip schedules. It now skips every cell that already has a recorded post.\n\n" +
		Fallback
	if m.Summary != want {
		t.Errorf("summary\n%s\nwant\n%s", m.Summary, want)
	}
	if m.Title != "opus schedule is re-runnable and statics pass the gate, and 2 more changes" {
		t.Errorf("title %q", m.Title)
	}
	if !reflect.DeepEqual(m.PRs, []int{21, 20, 19}) {
		t.Errorf("prs %v", m.PRs)
	}
	if m.Bump != Minor {
		t.Errorf("bump %s, want minor from #20", m.Bump)
	}
}

func TestComputeFirstRelease(t *testing.T) {
	g := &fakeGit{head: "h", commits: commits("initial import"), files: []string{"go.mod", "README.md"}}
	m := compute(t, g, Options{})
	if g.from != "" || m.Previous != "v0.0.0" || m.Version != "v0.1.0" || !m.Release {
		t.Errorf("from %q previous %s version %s release %v", g.from, m.Previous, m.Version, m.Release)
	}
	m = compute(t, &fakeGit{head: "h", commits: commits("manifest v2 (#23)"), files: []string{"go.mod"}}, Options{})
	if m.Version != "v1.0.0" {
		t.Errorf("first release with release:major = %s, want v1.0.0", m.Version)
	}
}

func TestComputeIgnoresNonReleaseTags(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: []string{"v0.9.0", "v0.10.0", "v0.11.0-rc1", "v2"},
		commits: commits("gate: sixth post (#21)"), files: []string{"x.go"}}, Options{})
	if m.Previous != "v0.10.0" || m.Version != "v0.10.1" {
		t.Errorf("previous %s version %s", m.Previous, m.Version)
	}
}

func TestComputeTagExistsFails(t *testing.T) {
	gh, _ := fixtureGH(t)
	_, err := Compute(context.Background(), &fakeGit{head: "h", tags: released, other: []string{"v0.2.2"},
		commits: commits("gate: sixth post (#21)"), files: []string{"x.go"}}, gh, Options{})
	var rerr *Error
	if !errors.As(err, &rerr) || !strings.Contains(err.Error(), "tag v0.2.2 already exists") {
		t.Fatalf("err = %v, want a *Error naming v0.2.2", err)
	}
}

func TestComputeTagExistsIgnoredWhenNotReleasing(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released, other: []string{"v0.2.2"},
		commits: commits("docs: operator guide (#22)"), files: []string{"docs/a.md"}}, Options{})
	if m.Release {
		t.Fatal("released")
	}
}

func TestComputeVersionOverride(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released,
		commits: commits("gate: sixth post (#21)"), files: []string{"x.go"}},
		Options{Version: "v0.5.0", Force: true})
	if m.Version != "v0.5.0" || !m.Release || !strings.Contains(m.Reason, "version v0.5.0 requested") {
		t.Errorf("version %s release %v reason %q", m.Version, m.Release, m.Reason)
	}

	gh, _ := fixtureGH(t)
	for _, v := range []string{"v0.2.1", "v0.1.9", "0.3.0", "v0.3"} {
		_, err := Compute(context.Background(), &fakeGit{head: "h", tags: released,
			commits: commits("gate: sixth post (#21)"), files: []string{"x.go"}}, gh, Options{Version: v, Force: true})
		var rerr *Error
		if !errors.As(err, &rerr) {
			t.Errorf("version %s: err = %v, want *Error", v, err)
		}
	}
}

func TestComputeNothingMerged(t *testing.T) {
	m := compute(t, &fakeGit{head: "h", tags: released}, Options{Force: true})
	if m.Release || !strings.HasPrefix(m.Reason, "nothing merged since v0.2.1; run it with a version") {
		t.Errorf("release %v reason %q", m.Release, m.Reason)
	}
	// A manual run with a version republishes the same commit.
	m = compute(t, &fakeGit{head: "h", tags: released}, Options{Force: true, Version: "v0.2.2"})
	if !m.Release || m.Version != "v0.2.2" || m.Summary != Fallback {
		t.Errorf("release %v version %s summary %q", m.Release, m.Version, m.Summary)
	}
}

func TestComputeLooksEachPROnce(t *testing.T) {
	gh, calls := fixtureGH(t)
	_, err := Compute(context.Background(), &fakeGit{head: "h", tags: released,
		commits: commits("gate: sixth post (#21)", "Revert gate (#21)"), files: []string{"x.go"}}, gh, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Errorf("gh called %d times: %v", len(*calls), *calls)
	}
	want := []string{"gh", "pr", "view", "21", "--json", "number,labels,title,body", "--repo", "michaellady/longform-pipeline"}
	if !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("gh args %v, want %v", (*calls)[0], want)
	}
}

func TestComputePRLookupFailureIsAnError(t *testing.T) {
	gh, _ := fixtureGH(t)
	_, err := Compute(context.Background(), &fakeGit{head: "h", tags: released,
		commits: commits("ghost (#404)"), files: []string{"x.go"}}, gh, Options{})
	if err == nil || !strings.Contains(err.Error(), "PR #404") {
		t.Fatalf("err = %v", err)
	}
	var rerr *Error
	if errors.As(err, &rerr) {
		t.Error("a gh failure is not a release-version error")
	}
}
