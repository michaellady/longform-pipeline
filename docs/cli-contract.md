# `longform` CLI contract

Every subsystem registers its subcommands in `internal/cli/cmd_<name>.go`
through `cli.Register`. This document is the contract between those
subsystems: what the watcher expects of each job-stage command and what the
ingest subsystem (`watch`, `stage`, `launch-session`, `notify`) guarantees to
the others.

## Shared rules

Verbatim from the build brief:

> Every job-stage command takes the job dir as its only positional arg, is
> idempotent, exits 0 on success and non-zero otherwise; stage commands
> advance `job.State` themselves.

Exit codes follow `internal/cli/registry.go` (sysexits): `0` ok, `64` usage,
`65` data/validation failure, `70` internal error, `75` temporary failure
(retry on the next pass). A command that cannot make progress yet, for
example `wait` while OpusClip is still rendering, exits `75` and leaves the
state untouched; the watcher logs it and tries again next pass.

Paths come from `internal/model/paths.go` and are overridable for tests and
the wizard dry run:

| Env var | Default | Holds |
|---|---|---|
| `LONGFORM_CONFIG_DIR` | `~/.config/longform-pipeline` | `profiles/<name>.json`, templates, corpora |
| `LONGFORM_DATA_DIR` | `~/Library/Application Support/longform-pipeline` | `jobs/<id>/`, `jobs/jobs.json` (dedupe index), `watch.lock` |
| `LONGFORM_LOG_DIR` | `~/Library/Logs/longform-pipeline` | `<id>.session.json`, `<id>.session.err`, launchd stdout/stderr |

## Job directory layout after `staged`

```
<DATA>/jobs/<YYYY-MM-DD>-<profile>-<hash8>/
├── job.json              owned by the binary (model.Job)
├── sources/01-<orig>     every original, moved (never copied-and-left, never deleted)
├── sources/02-<orig>     ... in recording order for stitch jobs
├── stitched.mp4          stitch jobs only: lossless concat, or re-encode
├── analysis/ review/ statics/   empty until later stages
└── logs/
    ├── events.log        one line per notify call: <RFC3339>\t<event>\t<message>
    └── <cmd>.log         stdout+stderr of every `longform <cmd> <dir>` the watcher ran
```

`job.json` fields the ingest subsystem writes (all others are left zero):

- `sources[]`: `id` (first 12 hex of the sha256), `path` (job-relative,
  `sources/NN-<original basename>`), `original` (inbox path it came from),
  `sha256`, `size_bytes`, `recorded_at` (ffprobe `creation_time`, falling back
  to mtime, UTC), `probe`.
- `probe`: `duration_ms`, `width`, `height`, `fps` (decimal string, at most
  three decimals: `"30"`, `"29.97"`), `codec` (`h264`, `hevc`), `audio_codec`,
  `color_transfer`, `rotation` (ffprobe display-matrix rotation as reported,
  e.g. `-90`; `0` when absent), `hdr` (`color_transfer` in
  `{smpte2084, arib-std-b67}`). `probe` is omitted only when ffprobe is not
  installed.
- `content_hash`: sha256 of the sorted source sha256s joined by `"\n"`.
  For a single source this is **not** the file hash.
- `stitched`: `true` when more than one source was concatenated.
- `extra.stitched_path` (`"stitched.mp4"`), `extra.stitched_mode`
  (`"copy"` or `"reencode"`), `extra.stitched_duration_ms`.
- `extra.session_launched_at` (RFC3339), `extra.session_pid`,
  `extra.session_stdout`, `extra.session_stderr`: written by `launch-session`.
- `history[]`: `stage` appends `detected -> staged`; a failure mid-stage
  appends `-> failed` with the error in `note` and `error`.

**Input for `prepare`:** `ingest.InputPath(job)` returns `<job>/stitched.mp4`
when `job.stitched` is true, otherwise `<job>/<sources[0].path>`. Other
packages should use that helper rather than re-deriving the rule.

**Dedupe index** (`<DATA>/jobs/jobs.json`): every source sha256 and the
content hash map to the job id. A file whose hash is already indexed is
skipped and left in the inbox (it is never deleted); the log names the job
it duplicates.

## `longform watch`

```
longform watch [--profile <name>|all] [--once] [--stable-seconds 60] [--interval 10m]
```

Brief, verbatim:

> for each profile (model.ListProfiles), scan profile.Inbox; for each
> candidate run the stage logic; then for every job not yet `clipped` exec
> in order `longform prepare <dir>`, `longform upload <dir>`,
> `longform wait <dir>` (same executable, os.Executable()), stopping at the
> first non-zero exit and logging to job logs/; when a job reaches `clipped`
> exec `longform launch-session <dir>` once (record `extra.session_launched_at`
> in job.json so it never launches twice). One job in flight per profile:
> skip starting a new job's upload while another job of that profile is
> between `uploaded` and `clipped`. `--once` runs one pass and exits
> (launchd WatchPaths mode); without it, loop every 10 minutes. Take a
> model.Lock named "watch" under model.DataRoot() so two launchd fires never
> overlap.

Details:

- A step runs only when the job is in the state that precedes it
  (`staged -> prepare`, `prepared -> upload`, `uploaded -> wait`), so an
  already-advanced job is not re-exec'd. After a step exits 0 the watcher
  reloads `job.json`; if the state did not advance it stops that job for the
  pass and logs it. A non-zero exit stops the job for the pass; nothing is
  marked failed by the watcher (the stage command owns that decision).
- Jobs are processed oldest first (`created_at`, then id). "In flight"
  means another job of the same profile is in state `uploaded`.
- Jobs in `failed`, `closed`, `detected` (staging crashed midway) or beyond
  `clipped` are left alone, except that a clipped-or-later job without
  `extra.session_launched_at` gets `launch-session` once per pass until it
  succeeds.
- Per-step output goes to `<job>/logs/<cmd>.log` with `=== <ts> longform <cmd> <dir>`
  and `=== exit <n>` markers; watcher progress goes to stderr (launchd's
  `StandardErrorPath`).
- `LONGFORM_SELF` overrides the executable the watcher execs (tests point
  it at a stub). The exec'd command inherits the watcher's environment, so
  launchd must set `PATH` (ffmpeg at `/opt/homebrew/bin`), `OPUSCLIP_API_KEY`
  and `LONGFORM_SLACK_WEBHOOK` in `EnvironmentVariables`; no login shell is
  involved. The launchd plist should also set `AbandonProcessGroup` to true
  so the detached claude session outlives the `--once` run (the session is
  started in its own session id regardless).
- Lock held: `<DATA>/watch.lock`. A second invocation exits `75` without
  doing anything; a stale lock from a dead pid is taken over.
- Exit `0` after a pass even when individual jobs failed a step; `70` when
  profiles or jobs could not be listed; `64` on bad flags.

## `longform stage`

```
longform stage --profile <name> [--file <path>...] [--stable-seconds 60] [--json]
```

One inbox pass for one profile (used by tests and the wizard dry run);
`--file` replaces the inbox scan with explicit files, which are **moved**
into the job like any inbox file (copy a bundled fixture first). Prints
`staged <id> <dir>` per job created, or the job list as JSON with `--json`.
Exit `65` when the profile is missing or invalid, `70` on an I/O failure.

Stage logic, verbatim from the brief, then details:

> candidates are regular files with video extensions (.mov .mp4 .m4v .MOV
> .MP4); call `brctl download <path>` and wait for the file to be
> materialised (no placeholder; `brctl` may be missing in tests, treat as
> no-op); require size and mtime unchanged for --stable-seconds (poll every
> 5 s; in --once mode a file that is still changing is simply skipped this
> pass); sha256 each file; skip hashes present in model.LoadIndex(); if
> profile.Stitch.Enabled group files whose recording time (ffprobe format
> tag creation_time, fallback mtime) falls within profile.Stitch.SessionWindowMin
> of each other, ordered by recording time, and losslessly concatenate with
> the ffmpeg concat demuxer and `-c copy` into one file, falling back to a
> re-encode (libx264 crf 18, aac) when `-c copy` fails; content hash of a
> job = sha256 of the sorted source hashes joined by newline; create the job
> with model.NewJob; MOVE each original into <job>/sources/ (os.Rename,
> fallback copy+remove across volumes) and write Source records with Probe
> filled from ffprobe (duration, width, height, fps, codec, audio codec,
> color_transfer, rotation, HDR = color_transfer in {smpte2084, arib-std-b67});
> add the hash to the Index; advance to `staged`; call notify.

- Extensions are matched case-insensitively. Dot-files are ignored except
  iCloud placeholders `.<name>.icloud`, which trigger `brctl download` on
  the real name; the real file is then watched for the stability window.
  `brctl download` is only invoked for placeholders and for paths under
  `Library/Mobile Documents/` (iCloud Drive).
- Stability: all candidates are polled together, so one pass waits one
  window, not one per file. In `--once` mode (always on for `stage`) a file
  that changes, or a placeholder that has not materialised by the end of the
  window, is skipped this pass. In loop mode the window restarts on change
  and a file is given up after an hour.
- For a stitch profile, if any candidate was skipped the whole pass is
  deferred, because a batch of class segments arrives over several seconds
  and grouping the early ones would split the session into two jobs.
- Grouping chains consecutive files whose recording times are within the
  window of each other (A..B 60 min, B..C 60 min with a 90-minute window
  is one session).
- Stitch: `-c copy` through the concat demuxer, validated by probing the
  output (duration must be within max(1 s, 2 %) of the input sum). Inputs
  that differ in codec, geometry, fps, rotation, audio codec or colour
  transfer go straight to the re-encode, which normalises to the first
  input's displayed geometry and fps through the `concat` filter (libx264
  crf 18, aac 192k). A single-file group is not stitched.
- Files ffprobe rejects are skipped (not a job); if ffprobe is absent the
  job is staged without a probe and the stitch falls back to `-c copy` only.
- A failure after the job directory exists (move, stitch, index) marks the
  job `failed` with the error and notifies `failed`; the moved sources stay
  in `sources/`.
- `notify` is called with event `staged` and message `<n> source(s)[, stitched (<mode>)]`.
- Stage output is deterministic for a given inbox: candidates sorted by
  path, groups by recording time, source order inside a group by recording
  time then path.

## `longform launch-session`

```
longform launch-session [--force] [--claude <path>] <jobdir>
```

Brief, verbatim:

> runs `claude -p "/longform-pipeline:longform-publish <jobdir>" --permission-mode acceptEdits
> --output-format json` with stdout to model.LogRoot()/<jobid>.session.json
> and stderr to .session.err, non-blocking (start and return), recording pid
> and start time in job.extra.

Details: refuses (`65`) unless the job is at least `clipped`, prints and
exits `0` without launching when `extra.session_launched_at` is already set;
`--force` overrides both. The claude binary is `--claude`, then
`$LONGFORM_CLAUDE`, then `PATH`, then `~/.local/bin/claude` (`70` when none
is found). The process starts with the job directory as its working
directory (so `acceptEdits` covers `review/` writes) and in its own session
id so launchd does not reap it with the watcher. The log files are opened in
append mode, so a `--force` relaunch adds to them.

## `longform notify`

```
longform notify --job <jobdir> --event <string> [--message <text>]
```

Brief, verbatim:

> macOS notification via `osascript -e 'display notification ...'` when
> profile.Notify.MacOS; Slack via incoming webhook URL from env
> LONGFORM_SLACK_WEBHOOK when set (POST JSON {text}); never fail the caller
> if notifications fail. Export a Go function notify.Send(profile, job,
> event, message) for other packages.

Details: always appends `<job>/logs/events.log` first. The Slack text is
`[longform <profile>] <event>: <job id>: <message>` (the job id is prefixed
when the message does not already contain it). The macOS notification has
title `longform <profile>`, subtitle `<event>`, body the message. Failures
are printed to stderr and the command still exits `0`; only bad flags (`64`)
or an unreadable job (`65`) are non-zero. The profile is loaded by
`job.profile`; when it is missing only Slack and the events log fire.
`LONGFORM_OSASCRIPT` overrides the osascript binary for tests. The webhook
URL is a secret and is never printed.

`notify.Send(p *model.Profile, j *model.Job, event, message string) error`
accepts nil for either argument and returns the joined channel errors for
logging only.

## What the other job-stage commands must honour

- `prepare <dir>`: input is `ingest.InputPath(job)`; advance `staged -> prepared`;
  re-running on a `prepared` or later job is a no-op exit `0`.
- `upload <dir>`: advance `prepared -> uploaded`; record both project ids
  before anything else so a re-run never re-submits.
- `wait <dir>`: advance `uploaded -> clipped` when both renders are done;
  exit `75` and leave the state alone while they are not.
- Any stage may mark the job `failed` (`job.Advance(model.StateFailed, by, note)`);
  the watcher then leaves it alone until a human resets it.
- All of them may call `notify.Send` for their own events; the watcher does
  not notify on their behalf.
