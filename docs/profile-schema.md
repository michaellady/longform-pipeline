# Profile schema

One JSON file per brand at `~/.config/longform-pipeline/profiles/<name>.json`
(`LONGFORM_CONFIG_DIR` overrides the root). The Go struct is
`internal/model/profile.go`; this page is the human-readable twin. `/longform-pipeline:longform-setup`
writes every field; you can edit the file by hand and re-run `longform status` to
validate (a bad profile is reported, never silently defaulted).

Paths may start with `~`; the binary expands it. Unknown top-level keys are an error;
put anything extra under `extra`.

## Top level

| Field | Type | Default | Meaning |
|---|---|---|---|
| `name` | string | file name | Profile slug; also the job id infix and the inbox folder name. |
| `brand` | object | required | See Brand. |
| `inbox` | string | required | Folder the watcher reads. The wizard uses `~/Library/Mobile Documents/iCloud~is~workflow~my~workflows/Documents/longform-inbox/<name>`. |
| `photos` | object | `{enabled:false}` | Second feed: an Apple Photos album. See Photos. |
| `orientation` | `portrait` \| `landscape` | `portrait` | What the source recording is. Drives the full-video render aspect. |
| `stitch` | object | `{enabled:false}` | See Stitch. |
| `prepare` | object | `{encoder:"auto"}` | See Prepare. |
| `stabilize` | object | `{enabled:false}` | See Stabilize. |
| `censor` | object | `{enabled:false, mode:"beep"}` | See Censor. |
| `transcribe` | object | `{language:"en"}` | See Transcribe. |
| `opusclip` | object | required | See OpusClip. |
| `cadence` | map platform -> int | `{youtube:2, instagram:1, linkedin:1, tiktok:2, facebook:1}` | Posts per day per platform. `0` disables a platform. Validation rejects negatives. |
| `slots_local` | string[] `HH:MM` | `["09:00","12:00","15:00","18:00","21:00"]` | Posting times in `timezone`. `slots propose` fills them in order; the full video takes the next `09:00` (first slot). |
| `timezone` | IANA tz | `America/Los_Angeles` | For `slots_local` and `publish_at`. |
| `backlog_days` | int | `25` | Held clips older than this are expired by `manifest expire`. |
| `cta` | object | `{}` | See CTA. |
| `copy` | object | `{}` | See Copy. |
| `buffer` | object | `{}` | See Buffer. |
| `statics` | object | `{}` | See Statics. |
| `notify` | object | `{macos:false}` | See Notify. |
| `extra` | object | omitted | Free-form; the ticket 0 probes write `youtube_native_chapters` here. |

## `brand`

| Field | Type | Meaning |
|---|---|---|
| `display` | string | Human name used in captions, quote attribution, Shortcut name. |
| `site` | URL | Brand home; appears in the full-video description. |

## `stitch`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `false` | Concatenate several inbox files into one segment (BJJ). |
| `session_window_min` | int | `90` when enabled | Files whose recording timestamps fall within this many minutes of each other form one segment. |

## `prepare`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `encoder` | `auto` \| `videotoolbox` \| `x264` | `auto` | Video encoder for prepare's re-encodes (HDR tonemap, stabilise pass). `auto`: the Mac's hardware `h264_videotoolbox` at `-q:v 65` when the local ffmpeg lists it and it passes a two-frame test encode, decoding with `-hwaccel videotoolbox` when that is listed too; otherwise `libx264`. `videotoolbox`: require the hardware encoder; prepare fails when it is unusable. `x264`: `libx264` CRF 18 `medium` with software decode. On a 4K HDR iPhone slice the hardware encoder ran about 2.7x faster than `libx264` and wrote under a third of the bytes (SSIM 0.99 luma against it). An HDR source is converted on VideoToolbox (`scale_vt`) whenever this Mac's ffmpeg can, whatever this field says: `scale_vt` takes only hardware frames, and those can only go to the hardware encoder. `x264` then applies to the stabilise pass only. Where VideoToolbox cannot convert (no `scale_vt`, or the hardware encoder fails its test), the conversion is `zscale` with this encoder, and without `zscale` prepare fails (`install.md`). Either way the output is H.264 and a converted file is tagged BT.709. The resolved setting is recorded under `extra.prepare.encoder` and `extra.prepare.hwaccel` in `job.json`, the HDR method under `extra.prepare.tonemap_filter` (`scale_vt` always decoded and encoded on VideoToolbox). A change that switches the encoder while a job is still `staged` re-runs that job's finished stabilise step, and its HDR conversion when that used `zscale`. |

## `stabilize`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `false` | Two-pass ffmpeg `vidstab`. |
| `shakiness` | 1..10 | `0` (ffmpeg default 5) | `vidstabdetect` shakiness. |
| `smoothing` | int | `0` (ffmpeg default 10) | `vidstabtransform` smoothing frames. |
| `zoom` | percent | `0` | `vidstabtransform` zoom to hide borders. |

## `censor`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `false` | Beep or mute strong profanity in `delivery.mp4` before upload; the gate also rejects listed words in titles, captions, hashtags. |
| `mode` | `beep` \| `mute` | `beep` | How a span is covered. |
| `extra` | string[] | `[]` | Words added to the plugin's `censor/default.json`. |

## `transcribe`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `model` | path | `""` (binary's bundled default) | whisper.cpp ggml model. |
| `language` | string | `en` | Passed to `whisper-cli`. |
| `threads` | int | `0` (auto) | CPU threads. |

## `opusclip`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `template_id` | string | `""` | Brand template applied to both projects. When probed the account had exactly one: `665fefb18ce49592eaeff8a0` ("Preset template 1": default, portrait, captions on, no screen overlay). |
| `strip_overlay` | bool | `false` | Run the free overlay-strip re-render on every short after curation. The skill only strips when the first clip's describe payload actually shows an overlay, so `true` is safe even with the overlay-free template. |
| `shorts` | object | see below | Curation for the shorts project. |
| `full` | object | see below | Render settings for the full-video project. |
| `accounts` | map key -> postAccountId | `{}` | Platform keys the skill and gates use: `youtube`, `instagram`, `facebook`, `linkedin_page`, `linkedin_profile`, `tiktok`, `x`, `threads`. Get ids with `longform opus accounts`. A platform must first be connected in the OpusClip dashboard (TikTok and X were not when probed); the skill produces no cells for a key that is missing or empty. |
| `sub_accounts` | map key -> id | omitted | For platforms that need a sub-account (Facebook page under a user). |

### `opusclip.shorts`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `model` | `ClipBasic` \| `ClipAnything` | `ClipBasic` | `ClipBasic` for talking heads; `ClipAnything` with a `prompt` for demonstrations. |
| `prompt` | string | omitted | Only with `ClipAnything`. |
| `durations` | int[] | `[30,60,90]` | Clip buckets as `[[0,30],[0,60],[0,90]]`. Never empty (empty would mean OpusClip "Auto"). |
| `aspect` | `portrait` \| `landscape` \| `square` | `portrait` | Shorts layout. |
| `remove_filler` | bool | `false` | Filler-word removal. |

### `opusclip.full`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `skip_curate` | bool | `false` (wizard sets `true`) | Render the whole video as one clip with captions and template. |
| `aspect` | string | `orientation` | Layout aspect for the full render. |

## `cta`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `lead_magnets` | map name -> URL | `{}` | Every entry is linked in link-platform captions and the full-video description with UTMs (`utm_source=<platform>`, `utm_medium=clip|longform`, `utm_campaign=longform_<slug>`, `utm_content=<clip_id>`). |
| `comment_to_dm` | map platform -> keyword | `{}` | On these platforms the CTA is "comment `<keyword>`" instead of a link. |
| `utm_source_by_platform` | bool | `false` | `true`: `utm_source` is the platform key; `false`: `utm_source=longform`. |

## `copy`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `style` | string | `""` | `evc` (hook + both lead magnets), `bjj-question` (one question for white and blue belts), anything else = plain hook + hashtags. |
| `voice_corpus` | path | omitted | JSON corpus of past captions the skill grounds voice on. |
| `boilerplate` | string | omitted | Appended on Instagram and TikTok captions (BJJ gym line, for example). |

## `buffer`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `channels` | map platform -> channel id | `{}` | Buffer channels for statics: `instagram`, `linkedin`, `facebook`, `threads`, optionally `bluesky`, `pinterest`. A cell platform resolves by exact key, else its family before the first `_` (`linkedin` covers `linkedin_page` and `linkedin_profile`). |
| `tags` | map format -> tag id | `{}` | Keys `video_quote`, `video_carousel`, `video_still`, `video_frame_carousel` for the Buffer tags `format:video-quote` etc. A static whose tag is missing is skipped. |

## `statics`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `templates_dir` | path | `""` | Folder with `<format>.html` exported from Claude Design (`docs/templates.md`). |
| `formats` | string[] | `[]` | Subset of `quote`, `carousel`, `still`, `frame_carousel` to produce per job. |

## `notify`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `slack_channel` | string | omitted | Channel id the brief link is posted to; the webhook itself lives in `~/.config/longform-pipeline/env` as `SLACK_WEBHOOK_URL`. |
| `macos` | bool | `false` | Fire a macOS notification on each event. |

## `photos`

The Photos album feed, next to the iCloud Drive `inbox`. The phone adds a
recording to the album (the "Mark for <Brand>" shortcut, or Share → Add to
Album), iCloud Photos syncs the bytes in the background, and every watcher pass
lists the album with `osxphotos`. Once a recording's original is on this Mac it
is cloned into `<DataRoot>/photos/incoming/` and staged exactly like an inbox
file; the Photos library itself is never changed. Until then the asset is
recorded as waiting and one macOS notification fires: "Waiting for <file>
(<size>) to finish syncing from iCloud Photos to this Mac". Feed state lives in
`<DataRoot>/photos/<name>.json`. Every recording that has synced by a pass is
staged in one batch, so `stitch` groups album recordings by recording time just
like inbox files; for a stitch profile the pass waits while any album recording
is still syncing, so a class's clips land in one job.

| Field | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `false` | Run the Photos pass for this profile. The wizard sets it when an album name is given. |
| `album` | string | `""` | Album name, exactly as in Photos (create it on the phone first). Only movies in it are fed. |
| `download_missing` | bool | `false` | Ask Photos to download an original that is still only in iCloud (`osxphotos export --download-missing --use-photokit`). Needs a second, Photos-access grant for the watcher, so it is off; the recommended setting instead is Photos → Settings → iCloud → "Download Originals to this Mac". The wizard keeps a hand-set value. |
| `osxphotos` | path | omitted | `osxphotos` executable. Empty = `$LONGFORM_OSXPHOTOS`, `PATH`, `~/.local/bin/osxphotos` (pipx), `/opt/homebrew/bin/osxphotos`, `/usr/local/bin/osxphotos`. The wizard records the one it found. |
| `library` | path | omitted | A Photos library other than the system library (`osxphotos --library`). The wizard keeps a hand-set value. |

osxphotos reads the Photos library database, which macOS guards with Full Disk
Access: the `longform` binary the LaunchAgent runs needs that grant once (System
Settings → Privacy & Security → Full Disk Access → + → the binary), and again
after an update replaces the file. Without it the pass logs "osxphotos cannot
read the Photos library" and `longform photos-watch` exits `69`.

## Example: `evc`

```json
{
  "name": "evc",
  "brand": { "display": "Enterprise Vibe Code", "site": "https://www.enterprisevibecode.com" },
  "inbox": "~/Library/Mobile Documents/iCloud~is~workflow~my~workflows/Documents/longform-inbox/evc",
  "photos": { "enabled": true, "album": "EVC", "download_missing": false },
  "orientation": "portrait",
  "stitch": { "enabled": false, "session_window_min": 90 },
  "stabilize": { "enabled": true, "shakiness": 5, "smoothing": 15, "zoom": 3 },
  "censor": { "enabled": true, "mode": "beep", "extra": [] },
  "transcribe": { "model": "", "language": "en", "threads": 0 },
  "opusclip": {
    "template_id": "665fefb18ce49592eaeff8a0",
    "strip_overlay": true,
    "shorts": { "model": "ClipBasic", "durations": [30, 60, 90], "aspect": "portrait", "remove_filler": true },
    "full": { "skip_curate": true, "aspect": "portrait" },
    "accounts": {
      "youtube": "pa_yt_evc", "instagram": "pa_ig_evc", "facebook": "pa_fb_evc",
      "linkedin_page": "pa_li_evc_page", "linkedin_profile": "pa_li_mike", "tiktok": "pa_tt_evc"
    }
  },
  "cadence": { "youtube": 2, "instagram": 1, "linkedin": 1, "tiktok": 2, "facebook": 1 },
  "slots_local": ["09:00", "12:00", "15:00", "18:00", "21:00"],
  "timezone": "America/Los_Angeles",
  "backlog_days": 25,
  "cta": {
    "lead_magnets": {
      "newsletter": "https://www.enterprisevibecode.com/subscribe",
      "discord": "https://www.enterprisevibecode.com/discord-server-invite"
    },
    "comment_to_dm": { "instagram": "newsletter", "tiktok": "newsletter" },
    "utm_source_by_platform": true
  },
  "copy": { "style": "evc", "voice_corpus": "~/.config/longform-pipeline/voice/evc.json" },
  "buffer": {
    "channels": { "instagram": "ch_ig", "linkedin": "ch_li", "facebook": "ch_fb", "threads": "ch_th" },
    "tags": { "video_quote": "tag_q", "video_carousel": "tag_c", "video_still": "tag_s", "video_frame_carousel": "tag_fc" }
  },
  "statics": { "templates_dir": "~/.config/longform-pipeline/profiles/evc/templates", "formats": ["quote", "carousel", "still", "frame_carousel"] },
  "notify": { "slack_channel": "C0123456789", "macos": true }
}
```

## Example: `sandbox-bjj`

```json
{
  "name": "sandbox-bjj",
  "brand": { "display": "Sandbox BJJ", "site": "https://www.sandboxbjj.com" },
  "inbox": "~/Library/Mobile Documents/iCloud~is~workflow~my~workflows/Documents/longform-inbox/sandbox-bjj",
  "orientation": "landscape",
  "stitch": { "enabled": true, "session_window_min": 90 },
  "stabilize": { "enabled": false, "shakiness": 0, "smoothing": 0, "zoom": 0 },
  "censor": { "enabled": true, "mode": "beep", "extra": [] },
  "transcribe": { "model": "", "language": "en", "threads": 0 },
  "opusclip": {
    "template_id": "tpl_sandbox_brand",
    "strip_overlay": false,
    "shorts": {
      "model": "ClipAnything",
      "prompt": "complete technique demonstrations and the single key detail the instructor emphasizes",
      "durations": [30, 60, 90], "aspect": "portrait", "remove_filler": false
    },
    "full": { "skip_curate": true, "aspect": "landscape" },
    "accounts": { "youtube": "pa_yt_bjj", "instagram": "pa_ig_bjj", "facebook": "pa_fb_bjj", "tiktok": "pa_tt_bjj" }
  },
  "cadence": { "youtube": 1, "instagram": 1, "linkedin": 0, "tiktok": 1, "facebook": 1 },
  "slots_local": ["09:00", "12:00", "17:00", "20:00"],
  "timezone": "America/Los_Angeles",
  "backlog_days": 25,
  "cta": { "lead_magnets": {}, "comment_to_dm": {}, "utm_source_by_platform": true },
  "copy": {
    "style": "bjj-question",
    "boilerplate": "Free trial class every Saturday at Sandbox BJJ. Link in bio."
  },
  "buffer": {
    "channels": { "instagram": "ch_ig_bjj", "facebook": "ch_fb_bjj" },
    "tags": { "video_quote": "tag_q", "video_carousel": "tag_c", "video_still": "tag_s", "video_frame_carousel": "tag_fc" }
  },
  "statics": { "templates_dir": "~/.config/longform-pipeline/profiles/sandbox-bjj/templates", "formats": ["still", "frame_carousel"] },
  "notify": { "macos": true }
}
```
