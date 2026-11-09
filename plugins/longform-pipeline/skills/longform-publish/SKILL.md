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
  (`claude -p "/longform-pipeline:longform-publish $JOB_DIR" --model opus --permission-mode acceptEdits`), so never
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
| YouTube cells: `title` = `short_title` ≤ 100 chars, `description` = the long caption. Facebook cells: `title` = `short_title` (≤ 255), `description` = the long caption. Every other platform: `title` = the long caption. | YouTube hard-caps the title; 25/25 posts once shipped broken from a caption-as-title. OpusClip refuses a Facebook title over 255 ("Shorten the title and put the full caption in the description"). | `brief parse-decision` maps Facebook cells; `longform gate validate` (exit 65 names the field) |
| Every clip and full-video caption ends with `[opus:<clip_id>]` on its own last line. | Grep-able attribution that survives manifest loss. | you, checked by the gate |
| With `profile.cta.static_cta == "newsletter-canon"`, every static caption on every channel ends with exactly the output of `longform brief newsletter-cta` (no `[opus:]` tag, no trailing punctuation after it). | The Comment-to-DM automation keys on that exact string and sends the newest newsletter. | `longform gate statics` |
| No static posts without a fresh `visual_review` PASS: an independent image-capable reviewer saw every final image beside its exact caption and the transcript passage. Any later caption or image change voids it. | Image availability and branding do not prove the image fits the caption. | `longform gate statics` (and `--hosted` before Buffer) |
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
   "platforms":[],"captions":{"facebook":""}},
 "clips":[{"project_id":"","clip_id":"","score":0,"duration_sec":0,"short_title":"",
   "status":"post|held","preview_url":"",
   "cells":[{"platform":"","account_id":"","sub_account_id":"","title":"","description":"",
     "hashtags":[],"publish_at":""}]}],
 "statics":[{"id":"","format":"quote|carousel|still|frame_carousel|<custom-html format>","platforms":[],"caption":"",
   "inputs":{"quote":"","attribution":"","timestamp_ms":0,
     "slides":[{"kind":"hook|section|quote|stat|cta","text":"","sub":"","timestamp_ms":0,
       "fields":{},"scene":"","illustration":""}],
     "pages":[]},
   "visual_review":{"reviewer":"","caption":"","verdict":"PASS|FAIL","evidence":"","source_excerpt":"",
     "images":[{"path":"","sha256":"","url":""}],"reviewed_at":""}}],
 "censored":[],
 "stabilization":{"before":"","after":""},
 "credits":{"spent":0,"to_spend":0},
 "decisions":[{"id":"","question":"","options":[],"default":""}]}
```

`publish_at` is RFC 3339 in `profile.timezone`. `platforms` on `full_video` lists the
profile account keys (`youtube`, `instagram`, `facebook`, `linkedin_page`, ...) the
full video goes to, plus `spotify` when the profile has a `spotify` block (the
Spotify video-podcast episode; full video only, never a clip or static platform). `account_id` is `profile.opusclip.accounts[<platform>]`;
`sub_account_id` is `profile.opusclip.sub_accounts[<platform>]` or `""`.
`full_video.captions` holds the post text for non-YouTube platforms (Facebook
always: its full-video cell gets `title` = `full_video.title`, `description` =
`captions.facebook`). `fields`/`scene`/`illustration` are for the
`newsletter-carousel` engine, `pages` for `custom-html`, `visual_review` for
every static (see Phase 5b). `docs/draft-schema.md` has every field.

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
   `spotify` is a full-video-only target when `profile.spotify.show_id` is set (it has
   no OpusClip account and needs no cadence entry).
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
| `facebook` | `short_title` (≤ 255 chars) | full caption |
| everything else | full caption | `""` |

`hashtags[]` duplicates the hashtags in the caption so the gate can check them.

### Phase 4 — Full-video package

- `project_id` = `clips.json.full_project_id`, `clip_id` = `clips.json.full_clip_id`.
- `title`: ≤ 100 chars, the video's claim.
- `captions`: for every non-YouTube full-video platform, its own post text
  (hook, CTA per Phase 3, hashtags, `[opus:<full_clip_id>]`), never the YouTube
  description with chapters. `captions.facebook` is required when Facebook gets
  the full video; its title is `title`.
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
  doubtful ones in `decisions[]` rather than guessing. Add `spotify` when the profile
  has a `spotify` block: `brief parse-decision` builds its cell itself (title =
  `title`, which must stay ≤ 200; description = the episode notes derived from
  `description` with `utm_source` set to `profile.spotify.notes_utm_source`, the
  `[opus:]` tag and hashtag-only lines removed, which must stay ≤ 4000 characters;
  the chapter lines become Spotify chapters). Write no `captions.spotify`.
- The full video is **scheduled natively as a clip**: the `skipCurate` project returns
  one clip of the full duration (probed: a 60 s file came back as one 60.0 s clip in
  under 3 minutes), so it gets ordinary cells like a short. The YouTube Data API path
  (`longform yt-upload`) is only the fallback for chapters and a custom thumbnail.

### Phase 5 — Statics (D17)

`profile.statics.engine` decides how statics are made. Every engine shares
these rules:

- `platforms` come from `profile.buffer.channels` keys.
- Caption: with `profile.cta.static_cta == "newsletter-canon"`, run
  `longform brief newsletter-cta --job <jobdir>` once and end **every** static
  caption with its output, after a blank line, nothing after it. The title in
  it is the newest newsletter's (from the voice corpus), not the video's: the
  Comment-to-DM flow sends the newsletter. Above it: the strongest verbatim
  quote in double quotes, or one grounded line. Otherwise follow Phase 3 for
  that platform set. One caption per static serves all its platforms; if a
  platform needs different text, make it a separate static.
- Quotes are **verbatim** transcript spans. Section, hook and stat copy is
  grounded in the transcript and checked against `profile.copy.voice_corpus`
  (sentence rhythm, vocabulary, first-person stance); a stat is a number the
  transcript states.

**Engine `templates` (default).** One entry per format in
`profile.statics.formats`; rendered after approval by `brief render-statics`.

- `quote`: `inputs.quote` is a **verbatim** transcript sentence ≤ 260 chars,
  `attribution` = `profile.brand.display`, `timestamp_ms` of the sentence start.
- `carousel`: `inputs.slides[]` = 1 `hook`, 3 `quote` (verbatim), 2 `stat` (a number the
  transcript actually states, with `sub` as the source phrase), 1 `cta`. Seven slides.
- `still`: `inputs.timestamp_ms` of one frame, caption band text in `caption`.
- `frame_carousel`: `inputs.slides[]` of 4–6 `section` entries each with a
  `timestamp_ms` and ≤ 90-char `text`.

**Engine `newsletter-carousel`.** The carousel-newsletter system in
`profile.statics.carousel_dir` (read its `SKILL.md` Phases 2–4 once). Formats:

- `carousel`: exactly 10 slides in carousel-newsletter Phase 2 order: 1 `hook`,
  2 `section`, 3 `quote`, 4 `stat`, 5 `quote`, 6 `section`, 7 `quote`,
  8 `stat` or `section`, 9 `section`, 10 `cta`. Per slide write `fields` for
  its template: hook `LINE_1` + `LINE_2` (≤ ~26 chars together), `TEASE`
  (≤ 120); section `KICKER`, `HEADLINE_1`, `HEADLINE_ACCENT`, `BODY` (≤ 220);
  quote `QUOTE` (verbatim, ≤ 260), `ATTRIBUTION` (`<video title> — <brand.display>`);
  stat `STAT_NUMBER`, `STAT_UNIT`, `STAT_LABEL_1`, `STAT_LABEL_2`; cta
  `HEADLINE_1`, `BODY`, `ACCENT_WORD` = `newsletter` (literally).
- `quote`: one `quote` slide, an illustrated quote card.
- `scene` on every slide: an illustration prompt that fills the frame edge to
  edge and *does* something tied to that slide's words (carousel-newsletter
  Phase 3 rules and examples). Never ask for text in the image.

Then render (this generates the illustrations: about $0.04 each, Gemini via
`gen_illustration.py` with the EVC banner reference; the command pauses 15 s
between calls):

```bash
longform brief render-carousel --job <jobdir> --illustrate
```

Exit 65 lists every problem (slide order, missing field, a quote that is not a
verbatim transcript span, quote over 260, accent word) before anything is
generated; fix `draft.json` and re-run. A re-run reuses existing
illustrations; delete one PNG under `statics/<id>/illus/` to regenerate just
that slide with a revised scene. Write `draft.json` with the statics first,
render, then continue.

**Engine `custom-html`.** You design each post. If
`profile.statics.reference_dir` is set, read its `README.md` (the design
principles) and every approved design under it (each subfolder's HTML
generator, rendered PNGs, opened with the Read tool, and captions) before
writing any HTML, and design the new posts in that visual language: same
layout grammar, type scale, palette use and caption shape, with this video's
content. Depart from it only when the user asks for something new (then say so
in `decisions[]`). Write one self-contained HTML
file per image under `statics/<id>/html/` (inline CSS, fixed 1080x1350 body,
colours only from `profile.brand.palette`, fonts from `profile.brand.fonts`,
wordmark `profile.brand.logo_text`; frames from `delivery.mp4` via
`ffmpeg -ss <sec> -i <jobdir>/delivery.mp4 -frames:v 1 statics/<id>/html/f-N.jpg`
referenced by relative path). List them in order in `inputs.pages`. Text on
the image follows the same grounding rules. Then:

```bash
longform brief render-html --job <jobdir>
```

Exit 65 names a missing page, a path outside the job or an unfilled
`{{TOKEN}}`. Open the PNGs yourself and iterate on the HTML until the layout is
clean before the independent review.

### Phase 5b — Visual review (newsletter-carousel and custom-html: required before the brief)

For each static, spawn **one fresh subagent** (Agent tool, `general-purpose`,
no other context) and give it: the absolute paths of every PNG
(`longform gate statics --job <jobdir> --list` prints them with sha256), the
exact final caption, and the transcript passage(s) the static draws on. Ask
it to open every PNG with its Read tool and answer JSON
`{verdict: PASS|FAIL, evidence: "<what it saw in the images>", issues: []}`:
does each image fit the caption and the passage, is any text in the image
wrong, cut off, or unreadable, is any illustration off-palette or showing stray
words, is the CTA slide's word `newsletter`. A filename, alt text or a
text-only review is not evidence.

Record its answer in the static as `visual_review`: `reviewer` (e.g. "fresh
general-purpose subagent"), `caption` (byte-identical to `statics[].caption`),
`verdict`, `evidence`, `source_excerpt`, `images` (`path` + `sha256` from
`--list`), `reviewed_at`. On FAIL fix the copy, scene or HTML, re-render and
review again (3 rounds max; then hold the static with a decision). Then:

```bash
longform gate statics --job <jobdir>
```

Exit 0, or fix what each verdict names. Any edit to a caption or an image
after this voids that static's review: the gate reports it as stale, and you
review again. For the `templates` engine this phase runs in the approval
phase after `render-statics` instead.

### Phase 6 — Adversarial review (must pass)

Assemble one prompt: the transcript, every cell's title and description, every static
caption, slide fields and scene prompt, each static's `visual_review` record, the
full-video title, description and captions, the profile's style rules, and the question "Is every claim grounded in the transcript, every CTA correct
per the profile, every YouTube title ≤ 100 chars, every Facebook title ≤ 255 with the caption in the description, every clip caption ending in `[opus:]`, every static caption ending in the canonical newsletter CTA (when newsletter-canon), every quote verbatim,
no clickbait, voice consistent? Answer JSON `{verdict: pass|fail, findings: []}`."

```bash
AR=/Users/mikelady/dev/claude-social-media-skills/_shared/adversarial-review/adversarial-review
if [ -x "$AR" ]; then printf '%s' "$PROMPT" | "$AR" --quiet; fi
```

For quote findings, check the claim yourself before editing: if the quote is a
byte-identical substring of the transcript text, it is verbatim (reviewers
hallucinate apostrophe mismatches). Any copy fix on a static re-runs its
render and Phase 5b.

If the binary is absent, spawn one fresh subagent (Agent tool, `general-purpose`) with
the same prompt and no other context. Fix every `fail` finding and re-run until the
merged verdict is `pass`. Never ship with a failing review; if it cannot pass after
three rounds, put the unresolved finding in `decisions[]` and hold the affected cells.

### Phase 7 — Slots, draft, brief, notify

1. Existing schedule: write `<jobdir>/review/existing.json` as an array of
   `{platform, account_id, publish_at}`. Source, in order of preference:
   the `opusclip_list_scheduled_posts` MCP tool when the OpusClip MCP is connected
   (the binary has no schedule-list command: OpusClip documents no listing endpoint
   for the REST key, and its undocumented `GET /publish-schedules?q=byPage` pages
   unpredictably); else `longform manifest schedule-ids` across recent jobs
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
   For the `newsletter-carousel` and `custom-html` engines run
   `longform gate statics --job <jobdir>` last: exit 0, or a static that cannot
   pass is removed from `statics[]` and named in a decision. Never render the
   brief over a failing static.
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
2. Engine `templates` only: `longform brief render-statics --job <jobdir>`
   renders PNGs (and the LinkedIn PDF and TikTok photo set for
   `frame_carousel`) into `statics/` from the profile's templates, then run
   Phase 5b (visual review) for every static and record `visual_review` in
   `draft.json`. The other engines rendered and reviewed in the draft phase;
   `render-statics` is a no-op for them.
3. `longform gate validate --profile <p> --cells <jobdir>/review/schedule.json`
   - exit 0: continue.
   - exit 65: stdout is a verdict JSON naming the cell, field, and limit. Fix exactly
     that field in `review/schedule.json` (shorten a title, strip a word, move a slot to
     the next day), re-run the gate. Never edit the gate, never skip it, never pass a
     flag that lowers a cap.
   - any other exit: stop and report.
4. `longform manifest init --job <jobdir>` (idempotent), then
   `longform manifest ensure-clip --job <jobdir>` for every clip in `schedule.json`.
5. `longform opus schedule --cells <jobdir>/review/schedule.json --job <jobdir>` (always
   pass `--job`) first reads `<jobdir>/manifest.json` and skips every cell that already
   has a recorded post: same `clip_id` and platform, an id, no `error`, one post per
   cell (the rule step 8's seal checks). Static cells are skipped as well (step 6 posts
   them): `schedule: static <id> goes through Buffer, skipped`. It posts the rest at
   1 req/s and records each successful response verbatim in `manifest.json` right after
   its POST, before the next cell, so a crash mid-step loses no post. Stdout carries one
   JSON line per newly scheduled or failed cell; a skipped cell prints nothing on stdout
   and one `schedule: skipped <label> on <platform>: already in manifest (<id>)` line on
   stderr; stderr ends with `schedule: <n> scheduled, <m> skipped` (`, <k> failed` when
   cells failed). Exit 65 means cells failed: their lines carry `error`, nothing is
   recorded for them, and seal lists them as missing; fix the cause and re-run this
   step.
   Piping stdout into `longform manifest append-post --job <jobdir>` is optional with
   `--job` and kept for backwards compatibility: the posted lines are already recorded,
   so `append-post` answers `append-post: duplicate <id> ignored` for each (exit 0) and
   only adds the failed attempts' lines, which give seal's verdict the API error.
   Re-running the approval phase from this step is safe: every cell OpusClip accepted is
   already in `manifest.json` and is skipped. A `spotify` cell is skipped too
   (`schedule: spotify full episode is a Spotify episode (no OpusClip route), skipped`):
   step 5b uploads it.
5b. **Spotify episode** (only when `schedule.json` has a `platform: "spotify"` cell).
   Spotify has no creator upload API and OpusClip cannot post there, so you upload it
   through the Spotify for Creators web UI in the **built-in browser** (the browser pane;
   the user is signed in there; read the `built-in-browser` skill first). Publishing is
   public content: nothing is scheduled or published until the operator says so in chat.
   1. **Already done?** If `manifest.json` already has a `transport: "spotify"` post for
      the full clip, skip this step. If an earlier run stopped after uploading, the
      show's episode list may already hold this title (status Draft or Scheduled):
      continue that episode instead of uploading a second one.
   2. **Prepare.** `longform spotify prepare --job <jobdir>`: writes
      `spotify/episode-1080p.mp4` (16:9; a vertical source is pillarboxed over a
      blurred fill), `spotify/episode-notes.txt` and `spotify/episode-notes.html`, and
      prints a JSON summary. Exit 65 means the title or notes are over Spotify's limits
      (200 / 4000 characters): shorten `full_video.title` or `description` in the
      schedule cell and the draft, re-run. Read `source_kind`: `delivery` means the
      uncaptioned prepared source was used because no fresh OpusClip export could be
      fetched (`source_note` says why); tell the operator, and re-run with
      `--source <file>` if they want the captioned export.
   3. **Hand the file to the operator.** The browser pane cannot attach a large file and
      cannot fetch from localhost, so the operator picks it in Spotify's file dialog:
      ```bash
      mkdir -p ~/Movies/longform-pipeline
      cp -c <jobdir>/spotify/episode-1080p.mp4 ~/Movies/longform-pipeline/<slug>-spotify.mp4
      ```
   4. **Open the show.** Navigate the browser pane to `profile.spotify.show_url` (default
      `https://creators.spotify.com/pod/show/<show_id>/home`). Read the episode list:
      note the latest episode number (the new one is that + 1). Click **+** (new
      episode), then **Upload**. Ask the operator in chat to choose
      `~/Movies/longform-pipeline/<slug>-spotify.mp4` in the "Select a file" dialog
      (file input `#uploadAreaInput`) and wait until they say it is selected and the
      upload has started.
   5. **Details.** Title = the spotify cell's `title`. Description: switch the
      description editor to **HTML**, then set the `textarea[name="description"]` to
      the contents of `spotify/episode-notes.html` with the native setter so React sees
      it:
      ```js
      const ta = document.querySelector('textarea[name="description"]');
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(ta, NOTES_HTML);
      ta.dispatchEvent(new Event('input', { bubbles: true }));
      ```
      (`NOTES_HTML` = the file's text as a JSON string literal.) Confirm Spotify shows
      "Timestamps detected" when the notes have chapter lines. Episode type Full, season
      number as the show uses it, episode number = latest + 1. Content checks
      (explicit etc.) stay **No** unless the transcript warrants otherwise (then add a
      decision line to your report). Thumbnail: take Spotify's automatic frame (images
      cannot be pushed from the pane; the auto thumbnail is acceptable). Comments On.
   6. **Review and time.** Go to **Review**. The schedule picker takes the **browser's
      local time**, not UTC (a UTC time was once typed as local). Read the browser's
      zone with
      `Intl.DateTimeFormat().resolvedOptions().timeZone` and `new Date().getTimezoneOffset()`,
      convert the cell's `publish_at` (UTC) into that zone, and choose **Schedule** with
      that date, hour, minute and AM/PM. If `publish_at` is already past, ask the
      operator (Now, or a new time) instead of guessing. Screenshot the Review step and
      show the operator, in chat: title, episode number, the time in UTC, in
      `profile.timezone` and in the browser's zone, and the file name. Then ask for a
      go-ahead.
   7. **Publish only after the operator confirms in chat**: click **Schedule** (or
      **Publish** when they chose Now). The episode list then shows the episode as
      "Scheduled"; its URL `/pod/show/<show_id>/episode/<episode_id>/wizard` carries
      the episode id. Public share links exist only once it is live.
   8. **Record.** One line to `longform manifest append-post --job <jobdir>`:
      ```bash
      jq -c --arg id "<episode_id>" --arg url "<wizard URL>" --arg local "<time as shown>" \
        '{cell: (.cells[] | select(.platform == "spotify")), transport: "spotify", post_id: $id,
          response: {status: "Scheduled", publish_local: $local, url: $url}}' \
        <jobdir>/review/schedule.json | longform manifest append-post --job <jobdir>
      ```
      `post_id` is required: without a `transport: "spotify"` record the seal reports
      the cell missing.
6. Statics through Buffer. In this order, for the statics in `schedule.json`:
   1. **Gate.** `longform gate statics --job <jobdir>`: exit 0 or stop. It
      checks each scheduled caption against its `visual_review`, the image
      digests, the Instagram aspect range (0.8..1.91) and the canonical CTA. A
      caption you shortened for `gate validate` voids the review: review again.
   2. **Host.** Buffer needs public URLs. Use the newsletter convention:
      ```bash
      SK=<skills repo: the parent of profile.statics.carousel_dir, else ~/dev/claude-social-media-skills>
      D=$SK/generated/<YYYY-MM-DD>-longform-<slug>
      mkdir -p "$D/<id>" && cp <jobdir>/statics/<id>/slide-*.png "$D/<id>/"   # once per static
      git -C "$SK" add generated/ && git -C "$SK" commit -m "Add longform image assets: <slug>" && git -C "$SK" push
      SHA=$(git -C "$SK" rev-parse HEAD)
      ```
      URLs are `https://raw.githubusercontent.com/<owner>/<repo>/$SHA/generated/<YYYY-MM-DD>-longform-<slug>/<id>/slide-NN.png`,
      pinned to the commit SHA, never `main`. If the commit is refused because
      `_shared/adversarial-review` drifts from mike-skills, run
      `_shared/adversarial-review/sync.sh --apply`, commit that alone, then
      commit the images. Never `--no-verify`. Write each URL into
      `visual_review.images[].url` in `draft.json`, then
      `longform gate statics --job <jobdir> --hosted`: it downloads every URL
      and refuses bytes that differ from the reviewed file. Exit 0 or stop.
   3. **Overlap.** `mcp__buffer__list_posts` for each target channel (queued
      and the last 7 days sent), piped to
      `$SK/_shared/buffer-queue-check/buffer-queue-check --keywords "<distinctive 4-8 word phrases from the caption>,<newsletter title>"`.
      A hit is surfaced to the user before posting, not silently posted.
   4. **Build args.** Always with `buffer-post-prep`, never by hand:
      ```bash
      $SK/_shared/buffer-post-prep/buffer-post-prep --channel-id <profile.buffer.channels[platform]> \
        --service <instagram|facebook|linkedin|threads> --text "<exact reviewed caption>" \
        --format-tag <profile.buffer.format_tags[format]> --image-url "<slide 1 URL>" --image-alt "<title>"
      ```
      It takes the tag id from `_shared/buffer-post-prep/tag-ids.local.json`
      (`carousel` -> `format:carousel`, `verbatim_quote` ->
      `format:verbatim-quote`) so buffer-stats measures these posts with the
      newsletter's. Exit 75 = dead channel, skip it and say so; 64/65 = fix
      the input. For a multi-image post append every further slide in the same
      asset shape, in order:
      `jq --argjson urls '["<url2>",...]' --arg alt "<title>" '.assets += [$urls[] | {image: {url: ., metadata: {altText: $alt}}}]'`.
      Without `format_tags` in the profile, use `profile.buffer.tags["video_<format>"]`
      as `tagIds`. **Read the live `mcp__buffer__create_post` schema before the
      first call**; statics are images only, never assume a video asset shape.
   5. **Post.** One `mcp__buffer__create_post` per static per platform with the
      prepared args verbatim (`mode: addToQueue`). Handle timeouts, image-fetch
      failures and 429s as PATTERNS.md "Buffer create_post" says (retry a
      timeout once with the same payload; stop on 429). If the Buffer MCP
      exists only in the CLI config, drive it headlessly:
      `env -u CLAUDE_CONFIG_DIR claude -p "Call mcp__buffer__create_post with exactly these arguments: <json>" --allowedTools mcp__buffer__create_post,mcp__buffer__get_post,mcp__buffer__list_posts`,
      passing the args verbatim and reading the returned post.
   6. **Read back.** `mcp__buffer__get_post` for every returned id: compare
      text (exact), channel, every asset URL in order, tag ids and status with
      what you sent. A mismatch is a failure for that cell. Then
      `mcp__buffer__list_posts` with `status: [error]` for the channels: any of
      today's posts there is a failure.
   7. **Record.** For each post, one line to
      `longform manifest append-post --job <jobdir>`:
      `{"cell": <the static's cell from schedule.json>, "response": <the create_post reply verbatim>, "transport": "buffer", "post_id": "<the Buffer post id>"}`.
      `post_id` is required: without it the seal reports the cell missing.
7. Full video to YouTube: the native OpusClip cell (already scheduled in step 5 as an
   ordinary clip) is the default. Only if `profile.extra.youtube_native_chapters` is
   `false` (set by the ticket 0 probe: the native post cannot carry chapters or a custom
   thumbnail), instead run
   `longform yt-upload --file <jobdir>/delivery.mp4 --title "<title>" --description "<description>" --chapters-file <jobdir>/review/chapters.txt --thumbnail <chosen thumb> --privacy private --publish-at <publish_at>`
   and drop the native YouTube full-video cell before step 5.
8. `longform manifest seal --job <jobdir>` checks that every cell in
   `review/schedule.json` has a recorded post in `manifest.json` (a `scheduleId` for
   OpusClip cells, the Buffer `post_id` for statics, the episode id of a
   `transport: "spotify"` line for the Spotify cell), records the clips left in the
   backlog as `held`, and moves the job `approved -> scheduled`.
   - exit 0: continue (also when the job was already sealed).
   - exit 65: stdout lists the cells with no post, one JSON object per line
     (`clip_id`, `platform`, `label`, `reason`). Those cells were not posted: fix them
     and re-run step 5 (OpusClip; with `--job` the whole `schedule.json` is fine, the
     recorded cells are skipped), step 5b (Spotify) or step 6 (Buffer) for exactly those cells, then seal
     again. Pass `--allow-missing` only if the user decided to skip them; the skipped
     cells are recorded under `job.extra.seal.missing`.
   - any other exit: stop and report.
9. `longform notify --job <jobdir> --event scheduled --message "<n> cells scheduled, full video <publish_at>"`.
   This only notifies; the state already moved in step 8. On its next pass the
   watcher runs `longform monitor`, which reads every OpusClip `scheduleId` back
   from OpusClip and moves the job to `monitored`; once the backlog window
   (`profile.backlog_days`) is over and no held clip remains, it closes the job.

Print the manifest summary: cells scheduled per platform, first and last `publish_at`,
held count, anything the gate rewrote.

---

## Edge cases

- **Approval phase stopped before the seal step** (the job stays `approved`; the
  watcher logs "approved but not sealed" every pass and never finishes it): resume,
  do not restart. Run `longform manifest seal --job <jobdir>` first; exit 65 lists
  exactly the cells that have no post yet. Then re-run step 5 on the whole
  `schedule.json` with `--job` (recorded cells and statics are skipped, so only the
  listed OpusClip cells post, each recorded as it is made) and step 6 for the listed
  statics only, append their Buffer lines, and seal again.
- **Spotify step interrupted**: never upload twice. Check `manifest.json` for a
  `transport: "spotify"` post, then the show's episode list for the title; an
  episode left as a draft is finished from its wizard URL, a scheduled one only
  needs step 5b.8. `longform spotify prepare` is idempotent (it reuses the
  downloaded export and skips the encode when the file is up to date).
- **Profile has no `spotify` block but the draft lists `spotify`**: the gate refuses
  the cell (no account). Hold it (`hold spotify`) or add the show with
  `/longform-pipeline:longform-setup`.
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
- **Buffer tag id missing for a format**: with `buffer.format_tags`,
  `buffer-post-prep` warns and emits args without `tagIds`; do not post
  untagged silently: hold the static and add a decision. Without
  `format_tags`, skip that static and add a decision pointing at
  `/longform-pipeline:longform-setup` stage 6.
- **A custom-html format with no fitting format tag** (a single card that is
  neither a carousel nor a verbatim quote): never invent a tag value; add a
  decision (post untagged, or add the value to the skills repo's
  `format_tags.json` first).
- **Newest newsletter changed between draft and approval**: `gate statics`
  reports the CTA mismatch; update the captions, review again, post.
- **`hold <platform>` with statics**: statics on that platform are dropped too.
- **Slot collision reported by `slots propose`**: it has already rolled to the next
  free slot; just copy its output.
