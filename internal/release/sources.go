package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner runs one command in dir and returns its stdout. Tests replace it
// with fixtures; ExecRunner is the real one.
type Runner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// ExecRunner runs commands with exec, each bounded by timeout, and folds the
// command's stderr into the error it returns.
func ExecRunner(timeout time.Duration) Runner {
	return func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%s %s: timed out after %s", name, strings.Join(args, " "), timeout)
		}
		if err != nil {
			msg := strings.TrimSpace(errb.String())
			if msg == "" {
				msg = err.Error()
			}
			return nil, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
		}
		return out.Bytes(), nil
	}
}

// ExecGit reads history with the git CLI in Dir.
type ExecGit struct {
	Dir string
	Run Runner
}

func (g ExecGit) git(ctx context.Context, args ...string) (string, error) {
	out, err := g.Run(ctx, g.Dir, "git", args...)
	return string(out), err
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (g ExecGit) Resolve(ctx context.Context, ref string) (string, error) {
	out, err := g.git(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (g ExecGit) Tags(ctx context.Context, head string) ([]string, error) {
	out, err := g.git(ctx, "tag", "--list", "--merged", head, "v*")
	return lines(out), err
}

func (g ExecGit) Commits(ctx context.Context, from, head string) ([]Commit, error) {
	rng := head
	if from != "" {
		rng = from + ".." + head
	}
	out, err := g.git(ctx, "log", "--first-parent", "--reverse", "--format=%H%x1f%s", rng, "--")
	if err != nil {
		return nil, err
	}
	var cs []Commit
	for _, l := range lines(out) {
		sha, subject, _ := strings.Cut(l, "\x1f")
		cs = append(cs, Commit{SHA: sha, Subject: subject})
	}
	return cs, nil
}

func (g ExecGit) ChangedFiles(ctx context.Context, from, head string) ([]string, error) {
	var (
		out string
		err error
	)
	if from == "" {
		out, err = g.git(ctx, "ls-tree", "-r", "--name-only", head)
	} else {
		out, err = g.git(ctx, "diff", "--name-only", from, head, "--")
	}
	return lines(out), err
}

func (g ExecGit) TagExists(ctx context.Context, tag string) (bool, error) {
	out, err := g.git(ctx, "tag", "--list", tag)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == tag, nil
}

// GH looks PRs up with the gh CLI. An empty Repo lets gh infer the repo from
// the checkout's remote.
type GH struct {
	Repo string
	Run  Runner
}

func (g GH) PR(ctx context.Context, number int) (PR, error) {
	args := []string{"pr", "view", strconv.Itoa(number), "--json", "number,labels,title,body"}
	if g.Repo != "" {
		args = append(args, "--repo", g.Repo)
	}
	out, err := g.Run(ctx, "", "gh", args...)
	if err != nil {
		return PR{}, err
	}
	var pr PR
	if err := json.Unmarshal(out, &pr); err != nil {
		return PR{}, fmt.Errorf("gh pr view %d: %w", number, err)
	}
	if pr.Number != 0 && pr.Number != number {
		return PR{}, errors.New("gh pr view " + strconv.Itoa(number) + ": answered for #" + strconv.Itoa(pr.Number))
	}
	return pr, nil
}
