// Command release-meta decides what a merge to main releases. The release
// workflow (.github/workflows/release.yml) runs it on every push to main.
//
//	release-meta [--repo owner/name] [--head REF] [--dir DIR] [--bump patch|minor|major]
//	             [--version vX.Y.Z] [--force] [--timeout 30s]
//
// prints JSON {version, previous, bump, title, summary, release, reason, sha,
// prs, changes}. It reads git history in --dir and each merged PR's labels,
// title and body with `gh pr view`, and changes nothing.
//
//	release-meta notes --meta meta.json --checksums dist/checksums.txt [--repo owner/name]
//
// renders the release body (Summary first, technical details collapsed) to
// stdout.
//
// Exit codes: 0 ok (whether or not to release is in the JSON), 64 usage,
// 65 the requested or computed version cannot be released (tag exists, not
// newer), 70 git or gh failed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/release"
)

const (
	exitUsage    = 64
	exitData     = 65
	exitInternal = 70
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "notes" {
		return notes(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("release-meta", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "owner/name for gh (default $GITHUB_REPOSITORY, else gh infers it from the checkout)")
	head := fs.String("head", "HEAD", "commit to release (the merge commit on main)")
	dir := fs.String("dir", ".", "git checkout to read history from")
	bump := fs.String("bump", "", "force the bump level: patch, minor or major (empty or auto = from PR labels)")
	version := fs.String("version", "", "release exactly this version, e.g. v0.3.0 (empty = next after the previous tag)")
	force := fs.Bool("force", false, "release even when only docs changed or the PR has release:skip (manual runs)")
	timeout := fs.Duration("timeout", 30*time.Second, "timeout for each git or gh call")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "release-meta: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	b, err := release.ParseBump(strings.TrimSpace(*bump))
	if err != nil {
		fmt.Fprintln(stderr, "release-meta:", err)
		return exitUsage
	}
	v := strings.TrimSpace(*version)
	if v != "" {
		if _, err := release.ParseVersion(v); err != nil {
			fmt.Fprintln(stderr, "release-meta:", err)
			return exitUsage
		}
	}

	runner := release.ExecRunner(*timeout)
	m, err := release.Compute(context.Background(),
		release.ExecGit{Dir: *dir, Run: runner},
		release.GH{Repo: *repo, Run: runner},
		release.Options{Head: *head, Bump: b, Version: v, Force: *force})
	if err != nil {
		fmt.Fprintln(stderr, "release-meta:", err)
		var rerr *release.Error
		if errors.As(err, &rerr) {
			return exitData
		}
		return exitInternal
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		fmt.Fprintln(stderr, "release-meta:", err)
		return exitInternal
	}
	return 0
}

func notes(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("release-meta notes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	metaPath := fs.String("meta", "meta.json", "JSON printed by release-meta")
	sums := fs.String("checksums", "dist/checksums.txt", "combined sha256 file of the release assets")
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "owner/name for the download URL (default $GITHUB_REPOSITORY)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *repo == "" {
		fmt.Fprintln(stderr, "release-meta notes: --repo is required outside GitHub Actions")
		return exitUsage
	}
	raw, err := os.ReadFile(*metaPath)
	if err != nil {
		fmt.Fprintln(stderr, "release-meta notes:", err)
		return exitUsage
	}
	var m release.Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		fmt.Fprintf(stderr, "release-meta notes: %s: %v\n", *metaPath, err)
		return exitData
	}
	if m.Version == "" || strings.TrimSpace(m.Summary) == "" {
		fmt.Fprintf(stderr, "release-meta notes: %s has no version or summary\n", *metaPath)
		return exitData
	}
	sum, err := os.ReadFile(*sums)
	if err != nil {
		fmt.Fprintln(stderr, "release-meta notes:", err)
		return exitUsage
	}
	fmt.Fprint(stdout, release.Notes(m, *repo, string(sum)))
	return 0
}
