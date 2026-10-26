# Releasing

Releases are automatic. Every merge to `main` runs `.github/workflows/release.yml`,
which decides whether the merge warrants a release and, if so, builds the darwin
arm64 and amd64 tarballs and publishes a GitHub release. Nobody pushes tags; the
release creates its tag (`vX.Y.Z`) on the merge commit.

`make release-preview` on an up-to-date `main` prints the same decision the
workflow would make for it, without changing anything.

## How the version is chosen

`cmd/release-meta` (logic in `internal/release`) reads:

- **previous**: the highest `vX.Y.Z` tag reachable from the merge commit. With no
  tag at all the first release is `v0.1.0`.
- **bump**: from the labels on the PRs merged since `previous` (a squash merge's
  subject ends in `(#N)`; a merge commit's reads `Merge pull request #N`):

  | Label on the PR | New version after v0.2.1 |
  |---|---|
  | none | `v0.2.2` (patch) |
  | `release:minor` | `v0.3.0` |
  | `release:major` | `v1.0.0` |

  When several PRs ship together, the highest label wins. A merged commit with no
  PR number counts as a patch, and the run's `reason` says so.

Add the label before merging (PR sidebar → Labels). A missing label never fails
the workflow; it just means patch.

## What does not release

A merge does not release when either is true:

- everything changed since `previous` is documentation or CI config: `docs/**`,
  any `*.md`, `.github/**`;
- the merged PR has the `release:skip` label.

`release:force` on the merged PR releases regardless. Skipped changes are not
lost: the next merge that does release includes them, and its notes list every
PR since the previous release (each PR's title and Summary, newest last).

Each run's decision and the reason are on the run's summary page in the Actions
tab, and in its `meta.json` artifact.

## What the release says

- **Title**: `longform-pipeline vX.Y.Z: <PR title>` (", and N more changes" when
  several PRs ship together).
- **Body**: `## Summary` first, taken from the merged PR's own `## Summary`
  section (up to its next heading). Write that section for a non-technical
  reader, because it is relayed to clients as-is. A PR without one gets its title
  and a stock sentence that says what the tool is. Install steps, the commits
  since the previous version, and checksums sit underneath in a collapsed
  "Technical details" block.

## Manual runs and re-releasing

Actions → **release** → **Run workflow** on `main`. A manual run always releases
(docs-only changes and `release:skip` are ignored), and takes two optional inputs:

- **bump**: `patch`, `minor` or `major` instead of the PR labels.
- **version**: exactly this version, e.g. `v0.3.0`. It must be newer than the
  latest tag on `main`.

The workflow refuses a version whose tag already exists. To publish a version
again (a release that failed halfway, or one that needs new assets):

```bash
gh release delete vX.Y.Z --cleanup-tag --repo michaellady/longform-pipeline
```

then run the workflow with `version` = `vX.Y.Z`.

A run that failed before publishing (a build error, a GitHub hiccup) created no
tag, so "Re-run failed jobs" on that run is enough.

## Several merges at once

Release runs never overlap (`concurrency: release`). If more merges land while one
is running, GitHub keeps only the newest waiting run; that run's release covers
every PR since the previous tag, so nothing is left out.

## Locally

```bash
make release-preview              # what merging main would release, and why
make release-dry VERSION=v0.3.0   # both tarballs and sha256 files into dist/
```
