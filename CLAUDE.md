# Project conventions for coding agents

These layer on top of the global agent instructions and win on conflict.

## Git and merging

- Work on a branch and open a pull request. Never push `main` directly; a ruleset
  requires a PR with a green `go` check.
- **Merge on green without asking.** Once a PR's CI passes, squash-merge it. The
  repository owner granted this standing approval on 2026-11-01; no per-PR
  "approved to merge" is needed for this repository.
- Squash merges only, so the PR title and its `## Summary` become the release notes.
- Delete the branch and any worktree after the merge.

## Releases

Every merge to `main` runs `.github/workflows/release.yml`, which cuts the next
version and composes the notes from the merged PRs (see `docs/releasing.md`). Do not
push tags by hand. Docs-only merges skip the release; use the `release:minor`,
`release:major`, `release:skip` or `release:force` labels to steer it.

## Pull request descriptions

Start with a `## Summary` written for a non-technical reader (two to four honest
sentences on what changed and why it matters), then `## Verified` and `## Not verified`.
The Summary is relayed verbatim into the release.

## Tests

`go test ./...` must pass. Tests never call the real OpusClip or Buffer APIs; use the
fakes under `internal/opusclip` and the fixtures under `plugins/longform-pipeline/fixtures`.
