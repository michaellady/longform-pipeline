# Operator guide: what you do by hand

Everything else runs on the Mac. These are the only steps that need a person,
split into what you do once per brand and what you do per video.

## Once per brand

1. **Run the wizard** from the Claude you will use day to day (`/longform-pipeline:longform-setup`).
   It creates the profile, stores the OpusClip key, creates the iCloud inbox and
   installs the launchd watcher. Run it from the same Claude whose plugin and
   sign-in the headless session should use (see `install.md`).
2. **Connect the OpusClip connector** once: `/mcp` in a Claude session, or
   claude.ai → Settings → Connectors → OpusClip, then sign in on OpusClip's
   consent page.
3. **Connect every platform in OpusClip** that should receive clips:
   clip.opus.pro → menu → Social accounts → Add account. Re-run the wizard
   afterwards (or ask Claude) so the new account ids land in the profile.
4. **Create the four Buffer tags**, names exactly as written, in Buffer →
   Settings → Tags, then put their ids in the profile (`buffer.tags`):
   - `format:video-quote`
   - `format:video-carousel`
   - `format:video-still`
   - `format:video-frame-carousel`

   Without them clips and the full video still post; only the static image
   posts wait.
5. **Add the iPhone Shortcuts** for the profile. Give the wizard a Photos album
   name (stage 9) and create that album, same exact name, in Photos on the
   phone. The wizard builds and signs two shortcuts on the Mac: "Mark for
   <Brand>" (`longform shortcut --profile <p> --kind mark --album <name> --open`),
   which adds a recording to the album, and the fallback "Send to <Brand> inbox"
   (`longform shortcut --profile <p> --open`), which copies the file into the
   iCloud Drive inbox. Click "Add Shortcut" in the Shortcuts window for each and
   iCloud syncs them to the phone. Building them by hand on the phone takes five
   minutes: `shortcut-recipe.md`.
6. **Let the Mac read the album** (album profiles only), once:
   - System Settings → Privacy & Security → Full Disk Access → + → the
     `longform` binary path the wizard printed. Check it again after an update
     replaces the binary.
   - Photos → Settings → iCloud → "Download Originals to this Mac".
   - Leave the Mac awake and signed in with iCloud Photos on (Energy settings:
     prevent automatic sleeping when the display is off, if it is a desktop).
7. **Sign the CLI in** once in the same config directory the LaunchAgent uses:
   open `claude` from the app's terminal (or the matching alias) and run
   `/login`. `install.md` lists the symptoms if this is missed.

Optional: a Slack incoming webhook (wizard stage 8) mirrors each brief to Slack;
macOS notifications fire regardless.

## Per video

1. **Record** in the iPhone's standard Video mode. Portrait or landscape as the
   profile says (vertical for a walking monologue, horizontal for a class),
   1080p at 30 fps, one take. If a take is bad, re-record; there is no edit step.
2. **Share → "Mark for <Brand>"** (for example "Mark for EVC"), or in Photos
   Share → Add to Album → the profile's album. That is the whole hand-off: one
   tap, nothing is copied on the phone, and iCloud Photos uploads the recording
   in the background. For a stitch profile (several clips per class segment,
   joined into one video; sandbox-bjj, for example) add all of a class's clips
   to the album; the Mac waits until every one of them has arrived, then
   stages and stitches them into one job.

   Fallback: **Share → "Send to <Brand> inbox"** copies the file into the
   iCloud Drive inbox instead. Use it when the profile has no album, or when a
   recording must not wait for iCloud Photos. Use one route per recording, not
   both, and send all of a stitched class the same way.
3. **Wait.** The Mac picks the recording up within ten minutes of its full file
   arriving on the Mac. A large recording can take a while to sync from iCloud
   Photos; while it does, the Mac shows one notification, "Waiting for
   <file> (<size>) to finish syncing from iCloud Photos to this Mac", and keeps
   checking every ten minutes. Then it prepares the recording
   (stabilisation, transcript, profanity beep), sends it to OpusClip, waits for
   the clips and runs the editorial session. Allow about thirty minutes for a
   fifteen-minute video. `longform status` shows the job's state at any time.
4. **Read the brief.** A macOS notification (and Slack, if configured) says it
   is ready; it is `review/brief.md` in the job folder. Decisions you must make
   are listed last.
5. **Approve** from a Claude session:

   ```text
   /longform-pipeline:longform-publish approve <job-slug> "approve"
   ```

   Variants, combinable with `;`: `approve except 3,7` (hold those clip rows),
   `hold linkedin` (skip a platform), `drop 4`, `swap thumbnail 2`,
   `decide d1 <option>`, `reject <note>`. The note is everything after
   `reject`, commas and all, so make it the last clause.

Nothing posts until step 5. After approval the schedule is enforced by the
gates (platform caps, cadence, profanity in titles), the manifest records every
scheduled post, and the weekly flywheel measures the result.

## What happens after approval

You do nothing more; this is what the Mac does, and the one check that is
yours.

1. **Approval ends with a seal.** The approval session schedules every post,
   then checks that each one has a confirmation on file. If any post is
   missing it stops and lists them, and the job stays "approved" until they
   are posted (or you tell Claude to skip them). When everything is on file
   the job becomes "scheduled" and you get a notification.
2. **The Mac checks the schedule with OpusClip.** Within ten minutes the
   watcher asks OpusClip for each scheduled post and records the answer next
   to it. If OpusClip no longer has a post that should still be waiting, you
   get one notification ("scheduled posts cannot be found in OpusClip"), the
   job stays "scheduled", and the Mac keeps checking every ten minutes. If
   everything is there the job becomes "monitored".
3. **Held clips wait, then expire.** Clips you held back stay in the backlog
   for the profile's `backlog_days` (25 by default). After that they expire
   and the job is "closed". A closed job is finished; nothing more happens to
   it.

**What the Mac cannot check, and how you check it.** OpusClip's key-based
access can only see posts that are still waiting. Once a post's time has
passed, it cannot tell whether the post went out or failed, and it cannot see
Buffer (the image posts) at all. So after a post's time, look yourself: in a
Claude session ask "list my OpusClip scheduled posts for <job>" (the OpusClip
connector's `opusclip_list_scheduled_posts` shows each post as scheduled,
posted or failed, with the link and the failure reason), and look at the
Buffer queue for the image posts. The check runs once per job, right after
approval, so it confirms the schedule exists, not that each post went out.

## Suggested first run

Record a two-minute spoken test rather than a full video. When its brief
arrives, approve it with everything held except one platform, so the first
live schedule is small and visible. Then delete or let the backlog expire the
test job.

## When something is off

- A job stuck at `clipped` with no brief: the headless session could not run;
  `<LogRoot>/<job>.session.json` says why (`install.md`, "Symptoms"). Fix the
  cause, then `longform launch-session --force <jobdir>`.
- A recording marked on the phone never shows up: check that the album name in
  Photos matches the profile's `photos.album` exactly, that the Mac is awake with
  iCloud Photos on, and run `longform photos-watch --profile <p> --dry-run` to
  see what the Mac sees ("waiting" = the full file has not synced yet; "Download
  Originals to this Mac" fixes a recording that stays waiting).
- `watch.err.log` says `osxphotos cannot read the Photos library`: the binary
  launchd runs lost or never had Full Disk Access (an update replaced it); grant
  it again (step 6 above).
- A job stuck at `prepared` with `OPUSCLIP_API_KEY is not set` in
  `logs/upload.log`: the env file is missing; re-run wizard stage 3.
- The brief says a platform was skipped: that platform has no account id in the
  profile; connect it in OpusClip and re-run the wizard.
- A job stays `approved` and `watch.err.log` says "approved but not sealed":
  the approval session stopped before its last step. Ask Claude to resume the
  approval for that job; it runs `longform manifest seal --job <jobdir>`
  first, which lists exactly the posts still missing, and schedules only those.
- A "monitor_failed" notification, or a job stuck at `scheduled`:
  `logs/monitor.log` in the job folder names each post OpusClip no longer has.
  Check them with the OpusClip connector (`opusclip_list_scheduled_posts`),
  re-schedule the missing ones from a Claude session, and the next pass moves
  the job on. A warning that a post is "still pending over an hour after its
  publish time" means OpusClip never sent it; reconnect that social account in
  OpusClip.
