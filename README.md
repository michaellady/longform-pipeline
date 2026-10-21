# longform-pipeline

Record a long-form video on your phone, let OpusClip clip it, and fan out the full video, the shorts, and static posts to every social surface after one approval.

- `cmd/longform` is the deterministic transport: watch an inbox, prepare media, talk to OpusClip, enforce publishing gates, hand off to a Claude Code skill.
- `plugins/longform-pipeline` is the Claude Code plugin: the `longform-publish` editorial skill, the `longform-setup` wizard, the launchd template, and the released binary.
- Brand profiles live in `~/.config/longform-pipeline/profiles/`, never in this repo.

Design: see the plan this was built from (`longform-video-pipeline-plan.md` in the author's dev folder); `docs/` carries the profile schema, the CLI contract, and the Shortcut recipe.
