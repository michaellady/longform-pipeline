package release

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Labels a PR can carry to steer the release its merge triggers. A PR with
// none of them releases a patch version.
const (
	LabelMajor = "release:major"
	LabelMinor = "release:minor"
	LabelSkip  = "release:skip"
	LabelForce = "release:force"
)

// NoPrevious is the "previous" version reported when no vX.Y.Z tag is
// reachable from the head; the first release is then FirstVersion.
const NoPrevious = "v0.0.0"

// FirstVersion is the version of a repo's first release (unless a major bump
// is asked for, which gives v1.0.0).
var FirstVersion = Version{Minor: 1}

// Commit is one first-parent commit on main.
type Commit struct {
	SHA     string
	Subject string
}

// Label is a GitHub label as `gh pr view --json labels` prints it.
type Label struct {
	Name string `json:"name"`
}

// PR is the part of `gh pr view --json number,labels,title,body` this
// package reads.
type PR struct {
	Number int     `json:"number"`
	Title  string  `json:"title"`
	Body   string  `json:"body"`
	Labels []Label `json:"labels"`
}

// Has reports whether the PR carries the label.
func (p PR) Has(label string) bool {
	for _, l := range p.Labels {
		if strings.EqualFold(l.Name, label) {
			return true
		}
	}
	return false
}

// Git is the repository history the decision reads.
type Git interface {
	// Resolve returns the full commit sha of ref.
	Resolve(ctx context.Context, ref string) (string, error)
	// Tags lists the v* tags reachable from head.
	Tags(ctx context.Context, head string) ([]string, error)
	// Commits lists the first-parent commits in from..head, oldest first;
	// from "" means all of head's history.
	Commits(ctx context.Context, from, head string) ([]Commit, error)
	// ChangedFiles lists the paths that differ between from and head;
	// from "" means every file at head.
	ChangedFiles(ctx context.Context, from, head string) ([]string, error)
	// TagExists reports whether a tag of that name exists anywhere.
	TagExists(ctx context.Context, tag string) (bool, error)
}

// PRSource looks up a pull request by number.
type PRSource interface {
	PR(ctx context.Context, number int) (PR, error)
}

// Options are the overrides a manual (workflow_dispatch) run can pass.
type Options struct {
	Head    string // ref or sha to release; "" = HEAD
	Bump    Bump   // forced bump level; "" = from PR labels
	Version string // exact version to release; "" = the next one after previous
	Force   bool   // release even when only docs changed or release:skip is set
}

// Meta is what cmd/release-meta prints and the release workflow consumes.
type Meta struct {
	Version  string   `json:"version"`
	Previous string   `json:"previous"`
	Bump     Bump     `json:"bump"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Release  bool     `json:"release"`
	Reason   string   `json:"reason"`
	SHA      string   `json:"sha"`
	PRs      []int    `json:"prs"`
	Changes  []string `json:"changes"` // first-parent commit subjects since previous, oldest first
}

var (
	squashRE = regexp.MustCompile(`\(#([0-9]+)\)\s*$`)
	mergeRE  = regexp.MustCompile(`^Merge pull request #([0-9]+) from `)
)

// PRNumber returns the PR a merge commit subject names: "title (#N)" from a
// squash merge or "Merge pull request #N from ..." from a merge commit; 0
// when there is none.
func PRNumber(subject string) int {
	m := squashRE.FindStringSubmatch(subject)
	if m == nil {
		m = mergeRE.FindStringSubmatch(subject)
	}
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// DocsOnly reports whether a changed path is documentation or CI config,
// which alone does not warrant a release: docs/**, **/*.md, .github/**.
func DocsOnly(path string) bool {
	return strings.HasPrefix(path, "docs/") ||
		strings.HasPrefix(path, ".github/") ||
		strings.HasSuffix(path, ".md")
}

// Error is a version that cannot be released: its tag already exists, or a
// requested version is malformed or not newer than the previous one.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errorf(format string, a ...any) error { return &Error{msg: fmt.Sprintf(format, a...)} }

// Compute decides the release for head: the previous version, the bump and
// next version, whether to release, and the title and summary.
func Compute(ctx context.Context, git Git, prs PRSource, opt Options) (Meta, error) {
	head := opt.Head
	if head == "" {
		head = "HEAD"
	}
	sha, err := git.Resolve(ctx, head)
	if err != nil {
		return Meta{}, err
	}
	m := Meta{SHA: sha, PRs: []int{}, Changes: []string{}}

	tags, err := git.Tags(ctx, sha)
	if err != nil {
		return Meta{}, err
	}
	prev, hasPrev := Latest(tags)
	from := ""
	m.Previous = NoPrevious
	if hasPrev {
		from = prev.String()
		m.Previous = from
	}

	commits, err := git.Commits(ctx, from, sha)
	if err != nil {
		return Meta{}, err
	}

	// Every change since previous, oldest first; PRs are looked up once each.
	var (
		entries  []entry
		bare     []string // short shas of commits naming no PR
		headPR   PR
		seen     = map[int]PR{}
		labelled = map[Bump][]int{}
	)
	for i, c := range commits {
		m.Changes = append(m.Changes, c.Subject)
		n := PRNumber(c.Subject)
		if n == 0 {
			bare = append(bare, short(c.SHA))
			entries = append(entries, entry{Title: c.Subject})
			continue
		}
		pr, ok := seen[n]
		if !ok {
			pr, err = prs.PR(ctx, n)
			if err != nil {
				return Meta{}, fmt.Errorf("PR #%d (from commit %s): %w", n, short(c.SHA), err)
			}
			if pr.Number == 0 {
				pr.Number = n
			}
			seen[n] = pr
			m.PRs = append(m.PRs, n)
			title := strings.TrimSpace(pr.Title)
			if title == "" {
				title = c.Subject
			}
			entries = append(entries, entry{Number: n, Title: title, Summary: ExtractSummary(pr.Body)})
			switch {
			case pr.Has(LabelMajor):
				labelled[Major] = append(labelled[Major], n)
			case pr.Has(LabelMinor):
				labelled[Minor] = append(labelled[Minor], n)
			}
		}
		if i == len(commits)-1 {
			headPR = pr
		}
	}

	// Bump: forced, else the highest label among the PRs, else patch.
	var bumpWhy string
	switch {
	case opt.Bump != "":
		m.Bump = opt.Bump
		bumpWhy = "requested by the manual run"
	case len(labelled[Major]) > 0:
		m.Bump = Major
		bumpWhy = LabelMajor + " on " + refs(labelled[Major])
	case len(labelled[Minor]) > 0:
		m.Bump = Minor
		bumpWhy = LabelMinor + " on " + refs(labelled[Minor])
	case len(m.PRs) == 0 && len(commits) > 0:
		m.Bump = Patch
		bumpWhy = "no PR number found in the merged commit subjects (" + strings.Join(bare, ", ") + ")"
	default:
		m.Bump = Patch
		if len(m.PRs) > 0 {
			bumpWhy = "no " + LabelMinor + " or " + LabelMajor + " label on " + refs(m.PRs)
		} else {
			bumpWhy = "default"
		}
		if headPR.Number == 0 && len(commits) > 0 {
			bumpWhy += "; no PR number in the head commit subject"
		}
	}
	reasons := []string{"bump " + string(m.Bump) + ": " + bumpWhy}

	// Version: requested, else the next after previous.
	switch {
	case opt.Version != "":
		v, err := ParseVersion(opt.Version)
		if err != nil {
			return Meta{}, &Error{msg: err.Error()}
		}
		if hasPrev && !prev.Less(v) {
			return Meta{}, errorf("version %s is not newer than %s, the latest tag on this branch; pick a higher version", v, prev)
		}
		m.Version = v.String()
		reasons = append(reasons, "version "+m.Version+" requested by the manual run")
	case hasPrev:
		m.Version = prev.Next(m.Bump).String()
	case m.Bump == Major:
		m.Version = Version{Major: 1}.String()
	default:
		m.Version = FirstVersion.String()
	}

	// Release decision.
	var decision string
	switch {
	case len(commits) == 0 && !(opt.Force && opt.Version != ""):
		decision = "nothing merged since " + m.Previous
		if opt.Force {
			decision += "; run it with a version to publish this commit under a new number"
		}
	case headPR.Has(LabelForce):
		m.Release = true
		decision = LabelForce + " label on #" + strconv.Itoa(headPR.Number)
	case opt.Force:
		m.Release = true
		decision = "manual run"
	case headPR.Has(LabelSkip):
		decision = LabelSkip + " label on #" + strconv.Itoa(headPR.Number)
	default:
		files, err := git.ChangedFiles(ctx, from, sha)
		if err != nil {
			return Meta{}, err
		}
		code := 0
		for _, f := range files {
			if !DocsOnly(f) {
				code++
			}
		}
		if code == 0 {
			decision = fmt.Sprintf("only docs or workflow files changed since %s (%d files)", m.Previous, len(files))
		} else {
			m.Release = true
			decision = fmt.Sprintf("%d of %d changed files since %s are not docs or workflow files", code, len(files), m.Previous)
		}
	}
	m.Reason = strings.Join(append([]string{decision}, reasons...), "; ")

	if m.Release {
		exists, err := git.TagExists(ctx, m.Version)
		if err != nil {
			return Meta{}, err
		}
		if exists {
			return m, errorf("tag %s already exists; to publish it again delete the release and tag first (gh release delete %s --cleanup-tag), or run the workflow with a higher version", m.Version, m.Version)
		}
	}

	m.Title = composeTitle(entries)
	if m.Title == "" {
		m.Title = "longform-pipeline " + m.Version
	}
	m.Summary = composeSummary(entries)
	return m, nil
}

func refs(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = "#" + strconv.Itoa(n)
	}
	return strings.Join(s, ", ")
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
