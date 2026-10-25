---
name: longform-publish
description: "Turn one clipped long-form job into a scheduled fan-out: pick clips, write per-cell copy with CTAs, package the full video, choose statics, pass adversarial review, write review/draft.json and the brief, then (on approval) schedule through the gates. Triggers: /longform-pipeline:longform-publish <jobdir>, 'draft the brief for <job>', 'publish job <slug>', 'approve <slug> ...', 'longform approve', 'schedule the longform job'. Invoked headlessly by the launchd watcher via claude -p and interactively for approval."
user_invocable: true
---

# longform-publish

You are the **editorial** layer of the long-form pipeline. The `longform` Go binary
owns bytes, state transitions, caps, and API calls. You own judgment: which clips,
what words, which frames, which decisions to surface. You read the job folder and
the profile, you write exactly one file (`review/draft.json`), and you call the
binary for everything else.

Two entry points:

- `/longform-pipeline:longform-publish <jobdir>` — draft phase. Runs headless from launchd
  (`claude -p "/longform-pipeline:longform-publish $JOB_DIR" --permission-mode acceptEdits`), so never
  ask a question; put open questions in `decisions[]` instead. Ends at state `drafted`.
- `/longform-pipeline:longform-publish approve <slug> "<grammar>"` — approval phase, interactive.
  Ends at state `scheduled`.

`<jobdir>` is an absolute path or a job id under
`~/Library/Application Support/longform-pipeline/jobs/`. `<slug>` is the job id.

## Non-negotiables

| Rule | Why | Enforced by |
|---|---|---|
| Shorts are 30, 60, or 90 s buckets. Never OpusClip "Auto". | Auto emits >90 s clips that fail platform caps. | profile `opusclip.shorts.durations`; `longform gate validate` |
| Per-platform cadence from `profile.cadence`, never a 6th post on a day. Roll to the next day. | Diminishing returns past the cadence; the old flat 5/day cap is retired. | `longform slots propose`, `longform gate validate` |
| YouTube cells: `title` = `short_title` ≤ 100 chars, `description` = the long caption. Every other platform: `title` = the long caption. | YouTube hard-caps the title; 25/25 posts once shipped broken from a caption-as-title. | `longform gate validate` (exit 65 names the field) |
| Every caption ends with `[opus:<clip_id>]` on its own last line. | Grep-able attribution that survives manifest loss. | you, checked by the gate |
| The verbatim API response for every scheduled cell goes into `manifest.json`. | `hasConflict` and future fields must survive; attribution JOINs on `scheduleId`. | `longform opus schedule` + `longform manifest append-post` |
| Gate exit codes are read, never bypassed. Exit 65 means fix the named field and re-run. | A prompt rule is advisory; a binary in the path is a gate. | you |
| No profanity from `censor/default.json` or `profile.censor.extra` in any title, caption, or hashtag. | Titles and thumbnails are what demonetize. | `longform gate validate` |
| You write only `review/draft.json` (and `review/thumb-N.jpg`). State moves only through `longform` commands. | One owner per file. | job layout |

## Job folder contract

```
<jobdir>/
  job.json                       state, profile, slug, sources, opusclip ids (binary-owned)
  sources/                       originals, never touched
  delivery.mp4                   prepared, censored, stabilized source
  analysis/transcript.json       local whisper: words[] with start_ms, end_ms, text
  analysis/censored.json         beeped spans: word, start_ms, end_ms
  analysis/opus-transcripts.json per-clip OpusClip transcripts (secondary)
  clips.json                     {shorts_project_id, full_project_id, full_clip_id,
                                  clips[]{project_id, clip_id, title, description, hashtags,
                                          score, duration_sec, uri_preview, uri_export, is_bonus}}
  review/draft.json              YOU write this (schema below)
  review/brief.md                longform brief render
  review/decision.json           longform brief parse-decision
  review/schedule.json           longform brief parse-decision (the cells to schedule)
  review/thumb-1.jpg .. 3        your thumbnail candidates
  review/stabilize-before.jpg, stabilize-after.jpg
  statics/                       longform brief render-statics output
  manifest.json                  longform manifest *
  logs/
```

Profile: `~/.config/longform-pipeline/profiles/<profile>.json` (schema in
`docs/profile-schema.md`). `job.json.profile` names it.

## draft.json schema (write exactly this shape)

```json
{"schema_version":"1","job_id":"","profile":"",
 "full_video":{"project_id":"","clip_id":"","title":"","description":"",
   "chapters":[{"at_ms":0,"title":""}],
   "thumbnails":[{"path":"","chosen":false}],
   "platforms":[]},
 "clips":[{"project_id":"","clip_id":"","score":0,"duration_sec":0,"short_title":"",
   "status":"post|held","preview_url":"",
   "cells":[{"platform":"","account_id":"","sub_account_id":"","title":"","description":"",
     "hashtags":[],"publish_at":""}]}],
 "statics":[{"id":"","format":"quote|carousel|still|frame_carousel","platforms":[],"caption":"",
   "inputs":{"quote":"","attribution":"","timestamp_ms":0,
     "slides":[{"kind":"hook|section|quote|stat|cta","text":"","sub":"","timestamp_ms":0}]}}],
 "censored":[],
 "stabilization":{"before":"","after":""},
 "credits":{"spent":0,"to_spend":0},
 "decisions":[{"id":"","question":"","options":[],"default":""}]}
```

`publish_at` is RFC 3339 in `profile.timezone`. `platforms` on `full_video` lists the
profile account keys (`youtube`, `instagram`, `facebook`, `linkedin_page`, ...) the
full video goes to. `account_id` is `profile.opusclip.accounts[<platform>]`;
`sub_account_id` is `profile.opusclip.sub_accounts[<platform>]` or `""`.

---

## Draft phase

### Phase 1 — Read

1. `cat <jobdir>/job.json`. Require `state == "clipped"`. If `drafted` or later, stop
   and say so (idempotent re-run). If earlier, stop: the binary has not finished.
2. Read `clips.json`, `analysis/transcript.json`, `analysis/censored.json`,
   `analysis/opus-transcripts.json`, and the profile.
3. Read `profile.copy.voice_corpus` if set. Read `profile.copy.boilerplate`.
4. Credits: `longform opus usage` prints the binary's **running estimate**
   (2 x source minutes per job; there is no REST credit endpoint). Put that in
   `credits.spent`; `credits.to_spend` is 0 (overlay strip is free). The true balance
   is only visible through the MCP tool `opusclip_get_usage` after OAuth, or the
   dashboard; if the MCP is connected in this session, call it and prefer its number,
   and say in the brief which source the figure came from.
5. Target platforms = keys of `profile.opusclip.accounts` that have a non-empty id
   **and** a non-zero `profile.cadence` entry. A platform with no account id (TikTok and
   X until they are connected in the OpusClip dashboard) gets no cells anywhere: not for
   clips, not for the full video. Note skipped platforms in the brief via a decision.
6. Overlay (profile `opusclip.strip_overlay: true` only): the binary strips at
   `clipped`, but confirm. If `job.json.opusclip.overlay_stripped` is false and the
   first clip's describe payload in `analysis/opus-transcripts.json` shows an overlay,
   run `longform opus strip-overlay --project <shorts_project_id> --clip <clip_id> --wait`
   per short (it skips by itself when the payload shows no overlay; the only brand
   template, `665fefb18ce49592eaeff8a0`, has none, so expect it to skip).

### Phase 2 — Pick clips

- Candidates: `score >= 70`. Clips with `is_bonus: true` (or a `_bonus` suffix in the
  title) are candidates too, not auto-picks.
- Capacity: sum `profile.cadence[platform]` over the next 7 days starting the day after
  the full video's slot (D13: full video at the next 09:00 slot, clips from the
  following day). One clip fills one slot per platform it goes to; a clip goes to every
  platform in `profile.cadence` unless the content is platform-wrong (LinkedIn gets no
  pure-gag clip, for example).
- Fill with the top scores, then swap for **theme coverage**: no two adjacent slots on
  one platform make the same point. The transcript's section structure is your theme
  map.
- Everything else is `status: "held"` (the backlog; the binary expires it at
  `profile.backlog_days`).
- `short_title`: ≤ 100 chars, no clickbait, the clip's own claim. Start from
  `clips[].title` and fix it.

### Phase 3 — Copy per cell

For every `post` clip, one cell per target platform. Structure, in order:

```
<hook: 1–2 lines, the clip's sharpest sentence, in the speaker's voice>

<body: 0–3 lines of context or the one key detail>

<CTA line(s)>

<hashtags: 3–5, lowercase, no spaces>
[opus:<clip_id>]
```

Voice: ground every sentence on `analysis/transcript.json` and
`profile.copy.voice_corpus`. Quote the speaker's phrasing; never invent a claim the
transcript does not make. No em dashes, no "In this video".

**CTA rules by `profile.copy.style`:**

- `evc` (D16). Link platforms (`youtube`, `facebook`, `linkedin_page`,
  `linkedin_profile`, `threads`): both lead magnets from `profile.cta.lead_magnets`, each
  as `<url>?utm_source=<platform>&utm_medium=clip&utm_campaign=longform_<slug>&utm_content=<clip_id>`.
  `utm_source` is the platform key when `profile.cta.utm_source_by_platform` is true.
  Comment platforms (`instagram`, `tiktok`): one line, "Comment
  `<profile.cta.comment_to_dm[platform]>` and I'll DM you the link" (no URL in the
  caption).
- `bjj-question` (D19). The hook is **one provocative question aimed at white and blue
  belts** about the detail in the clip ("Why does your guard pass stall the second they
  frame?"). On `instagram` and `tiktok`, append `profile.copy.boilerplate` after the
  question and before the hashtags. No lead-magnet links.
- Any other style: hook + body + hashtags + tag, no CTA unless `lead_magnets` is set.

**Field mapping:**

| Platform | `title` | `description` |
|---|---|---|
| `youtube` | `short_title` (≤ 100 chars) | full caption |
| everything else | full caption | `""` |

`hashtags[]` duplicates the hashtags in the caption so the gate can check them.

### Phase 4 — Full-video package

- `project_id` = `clips.json.full_project_id`, `clip_id` = `clips.json.full_clip_id`.
- `title`: ≤ 100 chars, the video's claim.
- `description`: 3–6 lines summary, then both CTAs with `utm_medium=longform`
  (`utm_content=<full_clip_id>`), then chapters as `MM:SS Title` lines, then hashtags,
  then `[opus:<full_clip_id>]`.
- `chapters[]`: from the transcript's topic shifts, first at `at_ms: 0`, 5–12 total,
  titles ≤ 60 chars.
- Thumbnails, three candidates. If the OpusClip MCP is connected and exposes
  `opusclip_create_thumbnail_job`, use it with the full project id and record the
  returned paths. Otherwise frame grabs:
  ```bash
  ffmpeg -y -ss <sec> -i <jobdir>/delivery.mp4 -frames:v 1 -q:v 2 <jobdir>/review/thumb-N.jpg
  ```
  Pick timestamps where the speaker is mid-gesture and the frame is sharp (use the
  transcript to find emphatic sentences). Mark one `chosen: true`.
- `platforms`: every target platform (Phase 1 step 5) whose surface accepts the full
  duration (Threads never; LinkedIn and Instagram per the caps the gate knows). Put the
  doubtful ones in `decisions[]` rather than guessing.
- The full video is **scheduled natively as a clip**: the `skipCurate` project returns
  one clip of the full duration (probed: a 60 s file came back as one 60.0 s clip in
  under 3 minutes), so it gets ordinary cells like a short. The YouTube Data API path
  (`longform yt-upload`) is only the fallback for chapters and a custom thumbnail.

### Phase 5 — Statics inputs (D17)

One entry per format in `profile.statics.formats`:

- `quote`: `inputs.quote` is a **verbatim** transcript sentence ≤ 260 chars,
  `attribution` = `profile.brand.display`, `timestamp_ms` of the sentence start.
- `carousel`: `inputs.slides[]` = 1 `hook`, 3 `quote` (verbatim), 2 `stat` (a number the
  transcript actually states, with `sub` as the source phrase), 1 `cta`. Seven slides.
- `still`: `inputs.timestamp_ms` of one frame, caption band text in `caption`.
- `frame_carousel`: `inputs.slides[]` of 4–6 `section` entries each with a
  `timestamp_ms` and ≤ 90-char `text`.

`platforms` for statics come from `profile.buffer.channels` keys. `caption` follows
Phase 3 rules for that platform set (the comment-to-DM line is fine on Instagram).

### Phase 6 — Adversarial review (must pass)

Assemble one prompt: the transcript, every cell's title and description, every static
caption and slide text, the full-video title and description, the profile's style
rules, and the question "Is every claim grounded in the transcript, every CTA correct
per the profile, every YouTube title ≤ 100 chars, every caption ending in `[opus:]`,
no clickbait, voice consistent? Answer JSON `{verdict: pass|fail, findings: []}`."

```bash
AR=/Users/mikelady/dev/claude-social-media-skills/_shared/adversarial-review/adversarial-review
if [ -x "$AR" ]; then printf '%s' "$PROMPT" | "$AR" --quiet; fi
```

If the binary is absent, spawn one fresh subagent (Agent tool, `general-purpose`) with
the same prompt and no other context. Fix every `fail` finding and re-run until the
merged verdict is `pass`. Never ship with a failing review; if it cannot pass after
three rounds, put the unresolved finding in `decisions[]` and hold the affected cells.

### Phase 7 — Slots, draft, brief, notify

1. Existing schedule: write `<jobdir>/review/existing.json` as an array of
   `{platform, account_id, publish_at}`. Source, in order of preference:
   the `opusclip_list_scheduled_posts` MCP tool when the OpusClip MCP is connected
   (the binary has no schedule-list command: OpusClip exposes no listing endpoint to
   the REST key); else `longform manifest schedule-ids` across recent jobs
   (`longform status --json`); else an empty array `[]`.
2. Write the cells you intend to post (every `post` clip cell plus the full-video
   cells) to `<jobdir>/review/cells.json` with `publish_at: ""`.
3. `longform slots propose --profile <p> --cells <jobdir>/review/cells.json --existing <jobdir>/review/existing.json`
   prints the same cells with `publish_at` filled from `profile.slots_local` and
   `profile.cadence` (full video at the next 09:00, clips from the next day). Copy each
   `publish_at` back into `draft.json`.
4. Fill `censored` from `analysis/censored.json`, `stabilization` from
   `job.json.artifacts`, `credits` from Phase 1.
5. `decisions[]`: only choices that are genuinely yours to make for the user: thumbnail
   pick when unsure, a platform the caps make doubtful, a held clip you think deserves a
   slot. Each has an `id` (`d1`, `d2`, ...), 2–4 `options`, and a `default`.
6. Write `<jobdir>/review/draft.json`. Validate it is parseable JSON (`jq . file`).
7. `longform brief render --job <jobdir>` (writes `review/brief.md`, state `drafted`).
8. `longform notify --job <jobdir> --event drafted --message "Brief ready: <n> clips to post, <m> held, full video <title>"`.
9. **STOP.** Print the brief path and the approval command. Do not schedule anything.

---

## Approval phase

`/longform-pipeline:longform-publish approve <slug> "<grammar>"`

Grammar (clauses separated by `;`; `reject` must be the last clause):

| Phrase | Effect |
|---|---|
| `approve` | post every `post` clip, the full video, every static, with defaults for every decision |
| `approve except 3,7` | approve, but clips 3 and 7 (brief table numbers) become `held` |
| `hold linkedin` | approve, drop every cell on that platform (clips, full video, statics) |
| `drop 4` | clip 4 is removed, not held (will not enter the backlog) |
| `swap thumbnail 2` | approve with thumbnail candidate 2 `chosen` |
| `decide d1 <option>` | answer decision `d1`; combine by running once per decision before `approve` |
| `reject <note>` | state stays `drafted`, nothing is scheduled; everything after `reject` is the note, punctuation included; rerun the draft phase after edits |

Steps, in order, reading every exit code:

1. `longform brief parse-decision --job <jobdir> "<grammar>"` writes
   `review/decision.json` and `review/schedule.json` (the final cells), state `approved`.
   Exit 64 is a grammar error: show the table above and stop.
2. `longform brief render-statics --job <jobdir>` renders PNGs (and the LinkedIn PDF
   and TikTok photo set for `frame_carousel`) into `statics/` from the profile's
   templates.
3. `longform gate validate --profile <p> --cells <jobdir>/review/schedule.json`
   - exit 0: continue.
   - exit 65: stdout is a verdict JSON naming the cell, field, and limit. Fix exactly
     that field in `review/schedule.json` (shorten a title, strip a word, move a slot to
     the next day), re-run the gate. Never edit the gate, never skip it, never pass a
     flag that lowers a cap.
   - any other exit: stop and report.
4. `longform manifest init --job <jobdir>` (idempotent), then
   `longform manifest ensure-clip --job <jobdir>` for every clip in `schedule.json`.
5. `longform opus schedule --cells <jobdir>/review/schedule.json` posts each cell at
   1 req/s and prints one JSON line per cell with the verbatim response. For each line,
   `longform manifest append-post --job <jobdir>` with that line on stdin.
   Re-running after a crash is safe: the binary skips cells already in the manifest.
6. Statics through Buffer: for every static in `schedule.json`, one
   `mcp__buffer__create_post` per platform in its `platforms`, `mode: addToQueue`,
   `channelId` from `profile.buffer.channels[platform]`, media from `statics/<id>/`,
   `tags: [profile.buffer.tags["video_<format>"]]`. **Read the live
   `mcp__buffer__create_post` tool schema before the first call** and build the
   `assets` from it: the binary's `buffer-post-prep` only ever emits
   `{"image": {...}}` assets, the video asset shape is unconfirmed, so never assume
   `{type: "video"}`. Statics are images and PDFs; if a format needs a video asset,
   stop and surface it as a decision. Record each response via
   `longform manifest append-post --job <jobdir>` with `"transport":"buffer"`.
7. Full video to YouTube: the native OpusClip cell (already scheduled in step 5 as an
   ordinary clip) is the default. Only if `profile.extra.youtube_native_chapters` is
   `false` (set by the ticket 0 probe: the native post cannot carry chapters or a custom
   thumbnail), instead run
   `longform yt-upload --file <jobdir>/delivery.mp4 --title "<title>" --description "<description>" --chapters-file <jobdir>/review/chapters.txt --thumbnail <chosen thumb> --privacy private --publish-at <publish_at>`
   and drop the native YouTube full-video cell before step 5.
8. `longform notify --job <jobdir> --event scheduled --message "<n> cells scheduled, full video <publish_at>"`.
   The binary moves state to `scheduled`; the next watch tick verifies every
   `scheduleId` and moves to `monitored`.

Print the manifest summary: cells scheduled per platform, first and last `publish_at`,
held count, anything the gate rewrote.

---

## Edge cases

- **Re-run of the draft phase on a `drafted` job**: exit immediately; the user must
  `reject` first or edit `draft.json` by hand and run `longform brief render` again.
- **Fewer candidates than slots**: fill what you can; never lower the score floor below
  70 to pad. Say so in the brief via a decision.
- **No `_shared/adversarial-review` and no subagent tool** (pure `claude -p`): review
  yourself in a second pass with the same prompt, record `"self_reviewed": true` in
  `decisions[]` as `d0` so the brief shows it.
- **Transcript missing or empty**: use `analysis/opus-transcripts.json`; note it in a
  decision. If both are missing, stop and `longform notify --event failed`.
- **Censored words**: never reuse a censored word in copy, even bleeped. The gate
  rejects it anyway.
- **Clips over 90 s in `clips.json`**: hold them and add a decision; they came from a
  misconfigured project and the gate will reject them.
- **OpusClip MCP absent**: every call has a `longform opus ...` twin; prefer the binary
  in headless mode regardless.
- **Buffer tag id missing for a format**: skip that static, add a decision pointing at
  `/longform-pipeline:longform-setup` stage 6.
- **`hold <platform>` with statics**: statics on that platform are dropped too.
- **Slot collision reported by `slots propose`**: it has already rolled to the next
  free slot; just copy its output.
