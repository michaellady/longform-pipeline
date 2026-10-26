package release

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// gitRepo builds a throwaway repo: v0.1.0 (annotated) on the first commit,
// then a docs PR, a code PR, and a merge-commit PR whose branch commit must
// not show up in the first-parent history; v9.9.9 sits on an unmerged branch.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, body string) {
		t.Helper()
		p := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", path)
	}
	git("init", "-q", "-b", "main")
	write("go.mod", "module x\n")
	git("commit", "-q", "-m", "initial import (#1)")
	git("tag", "-a", "v0.1.0", "-m", "v0.1.0\n\nFirst release.")
	write("docs/a.md", "a\n")
	git("commit", "-q", "-m", "docs: a (#2)")
	write("x.go", "package x\n")
	git("commit", "-q", "-m", "code: x (#3)")
	git("checkout", "-q", "-b", "feat")
	write("y.go", "package x\n")
	git("commit", "-q", "-m", "feat internals")
	git("checkout", "-q", "main")
	git("merge", "-q", "--no-ff", "feat", "-m", "Merge pull request #4 from me/feat")
	git("checkout", "-q", "-b", "side", "v0.1.0")
	write("z.go", "package x\n")
	git("commit", "-q", "-m", "side")
	git("tag", "v9.9.9")
	git("checkout", "-q", "main")
	return dir
}

func TestExecGitAgainstARealRepo(t *testing.T) {
	dir := gitRepo(t)
	ctx := context.Background()
	g := ExecGit{Dir: dir, Run: ExecRunner(10 * time.Second)}

	sha, err := g.Resolve(ctx, "HEAD")
	if err != nil || len(sha) != 40 {
		t.Fatalf("Resolve = %q, %v", sha, err)
	}
	tags, err := g.Tags(ctx, sha)
	if err != nil || !reflect.DeepEqual(tags, []string{"v0.1.0"}) {
		t.Errorf("Tags = %v, %v; want only the reachable v0.1.0", tags, err)
	}
	cs, err := g.Commits(ctx, "v0.1.0", sha)
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, c := range cs {
		subjects = append(subjects, c.Subject)
	}
	want := []string{"docs: a (#2)", "code: x (#3)", "Merge pull request #4 from me/feat"}
	if !reflect.DeepEqual(subjects, want) {
		t.Errorf("Commits = %q, want %q (first-parent, oldest first)", subjects, want)
	}
	if cs[len(cs)-1].SHA != sha {
		t.Errorf("last commit %s is not head %s", cs[len(cs)-1].SHA, sha)
	}
	all, err := g.Commits(ctx, "", sha)
	if err != nil || len(all) != 4 {
		t.Errorf("Commits from the root = %d, %v; want 4", len(all), err)
	}
	files, err := g.ChangedFiles(ctx, "v0.1.0", sha)
	if err != nil || !reflect.DeepEqual(files, []string{"docs/a.md", "x.go", "y.go"}) {
		t.Errorf("ChangedFiles = %v, %v", files, err)
	}
	files, err = g.ChangedFiles(ctx, "", sha)
	if err != nil || !reflect.DeepEqual(files, []string{"docs/a.md", "go.mod", "x.go", "y.go"}) {
		t.Errorf("ChangedFiles from the root = %v, %v", files, err)
	}
	for tag, want := range map[string]bool{"v0.1.0": true, "v9.9.9": true, "v0.1.1": false} {
		if got, err := g.TagExists(ctx, tag); err != nil || got != want {
			t.Errorf("TagExists(%s) = %v, %v", tag, got, err)
		}
	}
	if _, err := g.Resolve(ctx, "no-such-ref"); err == nil {
		t.Error("Resolve(no-such-ref) succeeded")
	}
}

type mapPRs map[int]PR

func (m mapPRs) PR(_ context.Context, n int) (PR, error) { return m[n], nil }

func TestComputeAgainstARealRepo(t *testing.T) {
	dir := gitRepo(t)
	prs := mapPRs{
		2: {Number: 2, Title: "docs a", Body: "## Summary\nDocs."},
		3: {Number: 3, Title: "code x", Body: "## Summary\nCode.", Labels: []Label{{Name: LabelMinor}}},
		4: {Number: 4, Title: "feature y", Body: "## Summary\nFeature."},
	}
	m, err := Compute(context.Background(), ExecGit{Dir: dir, Run: ExecRunner(10 * time.Second)}, prs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Previous != "v0.1.0" || m.Version != "v0.2.0" || m.Bump != Minor || !m.Release {
		t.Errorf("%+v", m)
	}
	if !reflect.DeepEqual(m.PRs, []int{2, 3, 4}) || m.Title != "feature y, and 2 more changes" {
		t.Errorf("prs %v title %q", m.PRs, m.Title)
	}
	if m.Reason != "2 of 3 changed files since v0.1.0 are not docs or workflow files; bump minor: release:minor on #3" {
		t.Errorf("reason %q", m.Reason)
	}
}

func TestGHPRParsesGhJSON(t *testing.T) {
	var got []string
	gh := GH{Repo: "o/r", Run: func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return os.ReadFile(filepath.Join("testdata", "pr-23.json"))
	}}
	pr, err := gh.PR(context.Background(), 23)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 23 || pr.Title != "manifest v2" || !pr.Has(LabelMajor) || !pr.Has(LabelMinor) || pr.Has(LabelSkip) {
		t.Errorf("%+v", pr)
	}
	if strings.Join(got, " ") != "gh pr view 23 --json number,labels,title,body --repo o/r" {
		t.Errorf("command %q", got)
	}

	// Without a repo, gh infers it from the checkout.
	gh.Repo = ""
	if _, err := gh.PR(context.Background(), 23); err != nil || strings.Contains(strings.Join(got, " "), "--repo") {
		t.Errorf("command %q, %v", got, err)
	}
	// An answer for a different PR is refused.
	if _, err := gh.PR(context.Background(), 24); err == nil {
		t.Error("accepted #23's JSON for #24")
	}
}

func TestExecRunnerTimeoutAndStderr(t *testing.T) {
	run := ExecRunner(100 * time.Millisecond)
	if _, err := run(context.Background(), "", "sleep", "5"); err == nil || !strings.Contains(err.Error(), "timed out after 100ms") {
		t.Errorf("sleep 5: err = %v, want a timeout", err)
	}
	if _, err := ExecRunner(5*time.Second)(context.Background(), t.TempDir(), "git", "rev-parse", "--verify", "nope"); err == nil {
		t.Error("git outside a repo succeeded")
	}
}
