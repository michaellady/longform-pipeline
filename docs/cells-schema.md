# Cells schema

A *cell* is one (asset × platform) publish the pipeline is about to fire. The
skill writes the array once (`review/schedule.json`); the same file then flows
through `longform slots propose` → `longform gate validate` → `longform opus
schedule` with only `publish_at` and `status` changing along the way. Go types:
`internal/gates/cells.go` (`gates.Cell`).

## Shape

A JSON array. Every element:

| field | type | meaning |
|---|---|---|
| `kind` | `"clip"` \| `"full"` \| `"static"` | OpusClip short, the full video, or a Buffer static post |
| `clip_id` | string | OpusClip clip id (clips and full), or the static's id |
| `project_id` | string | OpusClip project the clip belongs to |
| `platform` | string | one of `youtube`, `instagram`, `facebook`, `linkedin_page`, `linkedin_profile`, `tiktok`, `threads`, `x`, `bluesky`, `pinterest` |
| `account_id` | string | OpusClip `postAccountId` (from `profile.opusclip.accounts`) or Buffer channel id |
| `sub_account_id` | string | OpusClip sub-account (page) id when the platform needs one |
| `title` | string | the *exact* title arg the schedule call will send |
| `description` | string | the *exact* description/caption arg |
| `hashtags` | string[] | hashtags appended to the post text; with or without the leading `#` |
| `duration_sec` | number | media length; `0` for statics |
| `publish_at` | string | RFC3339 **UTC**; empty before slots run |
| `label` | string | OpusClip's human `"<PLATFORM> <account>"` label; informational, also the fallback for `platform` when that is empty |
| `status` | string, optional | `"held"` when slots could not place the cell inside the backlog horizon; `"skipped"` when the profile has no connected account for the platform; absent otherwise |

`account_id` must equal the profile's account for the platform and transport:
`opusclip.accounts[platform]` for clips and the full video, `buffer.channels[platform]`
for statics. Facebook and LinkedIn share one OpusClip `postAccountId` across
pages and profiles (three Facebook pages; LinkedIn org page plus person
profile), so clip and full cells on `facebook`, `linkedin_page` and
`linkedin_profile` must also carry `sub_account_id`, and cadence is counted per
`(platform, sub_account_id)`.

Example:

```json
[
  {"kind":"full","clip_id":"FULL1","project_id":"P1","platform":"youtube",
   "account_id":"6946...","sub_account_id":"","title":"Scaling without the slop",
   "description":"Full session...","hashtags":["#ai","#engineering"],
   "duration_sec":1840,"publish_at":"","label":"YOUTUBE Enterprise Vibe Code"},
  {"kind":"clip","clip_id":"La4Wghg6IX","project_id":"P1","platform":"instagram",
   "account_id":"6946...","sub_account_id":"9139...","title":"Your next 50% gain...",
   "description":"","hashtags":["ai"],"duration_sec":28,"publish_at":"",
   "label":"INSTAGRAM_BUSINESS Enterprise Vibe Code"}
]
```

## Which field is the post text

The constraints table (`internal/gates/constraints.json`) says, per platform,
where the human-authored post text goes. On `youtube` and `pinterest`
`title_source` is `separate`: `title` is its own capped field (100 chars) and
the post text goes in `description`. Everywhere else the post text *is*
`title`. Getting this wrong is the 2026-05-31 bug the gate exists for.

## `longform slots propose`

```
longform slots propose --profile <p> --cells <file> [--existing <file>] [--from YYYY-MM-DD] [--approved-at RFC3339]
```

Fills `publish_at` on every cell whose `publish_at` is empty and prints the
whole array on stdout (summary on stderr). Rules (D12, D13):

- **Full cells** go at the next `09:00` local slot after `--approved-at`
  (default now) that is at least 2 hours away, and not before `--from`. If the
  platform's cadence is already spent that day (existing posts) the full cell
  rolls to the next day with room.
- **Unconnected platforms**: a cell whose platform has no account in the
  profile (no `opusclip.accounts` entry for clips/full, no `buffer.channels`
  entry for statics) is never placed; it gets `"status":"skipped"` and the
  gate rejects it until the account is connected and added to the profile.
- **Clip cells** start the day after the full video, fill `profile.slots_local`
  in order, at most `profile.cadence[platform]` per `(platform, sub_account_id)`
  per local day. `linkedin_page` and `linkedin_profile` fall back to
  `cadence.linkedin`; a platform with no cadence entry is held.
- **Existing cells** (`--existing`, same schema: the live OpusClip schedule plus
  the Buffer queue exported by the skill) and cells that already carry a
  `publish_at` count toward the day's cadence and occupy their slot.
- **Backlog horizon**: a clip that cannot be placed within
  `profile.backlog_days` days of the clip start day gets `"status":"held"` and
  no `publish_at`. `longform manifest expire` later drops held clips.
- **Static cells** start on day two (same day clips start), one per platform
  per day, in a slot that platform is not already using. They do not count
  toward cadence.
- Days are **local calendar days** in `profile.timezone`; DST keeps the
  wall-clock slot, so the UTC offset of `publish_at` shifts across a
  transition.

`--now RFC3339` overrides the clock for reproducible runs.

## `longform gate validate`

```
longform gate validate --profile <p> --cells <file> [--existing <file>] [--censor-list <path>]
```

Per cell, in this order:

| check | field | rule |
|---|---|---|
| (a) caps | `title`, `description`, `hashtags` | per-platform caps from `constraints.json` (rune counts); `title` is required on every platform (a skipCurate full video ships with null title metadata, so the skill must always write one); hashtag count cap; post text + ` #tag`s within the post-text cap; hashtags well-formed |
| (a') account | `account_id`, `sub_account_id` | the platform is connected in the profile and `account_id` matches it (fix hint: "connect the account in OpusClip and add it to the profile"); a static's account is its Buffer channel, by exact platform key else family key (`linkedin` for `linkedin_page`); `sub_account_id` present on facebook / linkedin_page / linkedin_profile clip and full cells |
| (b) duration | `duration_sec` | clip/full cells only: threads 300, linkedin_page/profile 600, instagram 900, tiktok 600, facebook 14400, youtube 43200, x 140 seconds; override with `profile.extra.max_duration_sec` (`{"platform": seconds}`) |
| (c) profanity | `title`, `description`, `hashtags` | no whole word (case-insensitive) from `censor/default.json` next to the plugin binary (`../censor/default.json` from the binary's directory) or `--censor-list`, plus `profile.censor.extra` when `censor.enabled` |
| (d) cadence | `publish_at` | cells plus `--existing` never exceed `cadence[platform]` per `(platform, sub_account_id)` on any local day (clip and full cells only) |
| (e) timing | `publish_at` | present, RFC3339, and in the future for every non-held clip and full cell; a static may have none (it goes into the Buffer queue), and one it carries is checked the same way |

Exit `0` when every cell passes (nothing on stdout). Exit `65` with one JSON
verdict per failing check on stdout:

```json
{"clip_id":"X","label":"YOUTUBE Enterprise Vibe Code","platform":"youtube","field":"title",
 "reason":"title length 214 exceeds youtube cap 100",
 "fix_hint":"youtube maps post-text to \"description\"; supply a separate <=100-char title, don't reuse the caption"}
```

Exit `64` on usage errors (missing profile, unreadable cells, an explicit
`--censor-list` that does not exist). A missing *default* censor list is a
warning on stderr, and the check runs on `profile.censor.extra` alone. An
unknown platform fails closed. The gate never fires anything.
