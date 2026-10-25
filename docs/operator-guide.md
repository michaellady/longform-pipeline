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
5. **Add the iPhone Shortcut** for the profile. The wizard builds and signs it
   on the Mac (`longform shortcut --profile <p> --open`); click "Add Shortcut"
   in the Shortcuts window once and iCloud syncs it to the phone. Building it by
   hand on the phone takes five minutes: `shortcut-recipe.md`.
6. **Sign the CLI in** once in the same config directory the LaunchAgent uses:
   open `claude` from the app's terminal (or the matching alias) and run
   `/login`. `install.md` lists the symptoms if this is missed.

Optional: a Slack incoming webhook (wizard stage 8) mirrors each brief to Slack;
macOS notifications fire regardless.

## Per video

1. **Record** in the iPhone's standard Video mode. Portrait or landscape as the
   profile says (vertical for a walking monologue, horizontal for a class),
   1080p at 30 fps, one take. If a take is bad, re-record; there is no edit step.
2. **Share → "Send to <profile> inbox".** That is the whole hand-off. For a
   stitch profile (several files per class segment) select all of them and send
   them in one go.
3. **Wait.** The Mac picks the file up within ten minutes, prepares it
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
   `decide d1 <option>`, `reject <note>`.

Nothing posts until step 5. After approval the schedule is enforced by the
gates (platform caps, cadence, profanity in titles), the manifest records every
scheduled post, and the weekly flywheel measures the result.

## Suggested first run

Record a two-minute spoken test rather than a full video. When its brief
arrives, approve it with everything held except one platform, so the first
live schedule is small and visible. Then delete or let the backlog expire the
test job.

## When something is off

- A job stuck at `clipped` with no brief: the headless session could not run;
  `<LogRoot>/<job>.session.json` says why (`install.md`, "Symptoms"). Fix the
  cause, then `longform launch-session --force <jobdir>`.
- A job stuck at `prepared` with `OPUSCLIP_API_KEY is not set` in
  `logs/upload.log`: the env file is missing; re-run wizard stage 3.
- The brief says a platform was skipped: that platform has no account id in the
  profile; connect it in OpusClip and re-run the wizard.
