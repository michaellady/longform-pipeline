# Sandbox BJJ design assets

Brand two of the pipeline (plan decision D17): a Brazilian jiu-jitsu gym, course and community whose class recordings become technique videos, shorts and static posts.

## Artifacts

- Design System "Sandbox BJJ": https://claude.ai/artifact/6SRkDqAHjgqeHKjqAVdjnk
  Tokens in two themes (dark mat is primary, light sand secondary), type scale, spacing, radii; brand book (README); components QuestionCard, CaptionBand, TechniqueSlideHeader, CTABlock; the logo under assets/Logos; a cover.
- Design canvas "Sandbox BJJ static post templates": https://claude.ai/artifact/VAq8aMoGNocmHtmf1mzU8A
  Four 1080x1350 artboards: Main (quote card), Carousel (a `kind` tweak switches hook/section/quote/stat/cta; `index`/`total` tweaks drive the counter), Still (frame + caption band), FrameCarousel (frame + band + counter). The design system's tokens are installed on the canvas.

Both are private until shared from the page's Share menu.

## Templates

Exported to `~/.config/longform-pipeline/profiles/sandbox-bjj/templates/`:

| file | placeholders |
| --- | --- |
| `quote.html` | `{{QUOTE}}` `{{ATTRIBUTION}}` `{{BRAND}}` `{{SITE}}` |
| `carousel.html` | `{{SLIDE_KIND}}` (class on the root `.post`: `hook`, `section`, `quote`, `stat`, `cta`) `{{TEXT}}` `{{SUB}}` `{{INDEX}}` `{{TOTAL}}` `{{BRAND}}` `{{SITE}}` |
| `still.html` | `{{FRAME}}` (an `<img src>`) `{{CAPTION}}` `{{BRAND}}` `{{SITE}}` |
| `frame_carousel.html` | `{{FRAME}}` `{{CAPTION}}` `{{INDEX}}` `{{TOTAL}}` `{{BRAND}}` `{{SITE}}` |

No other `{{ }}` token appears in any template (checked by the render tool). Per-kind meaning of `TEXT` / `SUB` on the carousel: hook = headline / eyebrow; section = section title / "Part N of M" eyebrow; quote = the line / attribution (the em dash is added by CSS); stat = the number / its label; cta = the pill text / the lead line.

Each template is self-contained: inline CSS, the logo as a `data:` JPEG (320px, ~50 KB), and a small inline script that shrinks the headline (never below its `data-fit-min`) when the copy would overflow its box. Fonts load from Google Fonts: `https://fonts.googleapis.com/css2?family=Bebas+Neue&family=Inter+Tight:wght@500;600;700;800;900&display=block` (Inter Tight for sentences, Bebas Neue for labels, counters, wordmark and site line). Fallbacks: Inter, system-ui; Oswald, Impact. Rendering needs network access for the fonts; `--virtual-time-budget` in headless Chrome lets them load before the screenshot.

Brand facts used: the call to action is Comment "sandbox" below (from the old Chrome extension's boilerplate, pointing at the @sandboxbjj course + community); the caption style is one provocative question aimed at white and blue belts. No site URL was found locally, so `{{SITE}}` stays a placeholder for the profile to fill (the sample renders use `@sandboxbjj`).

## Verification

Rendered with headless Chrome at `--window-size=1080,1350`; every PNG measured 1080x1350:

- /private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design-bjj/quote.png
- /private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design-bjj/carousel-hook.png
- /private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design-bjj/carousel-section.png
- /private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design-bjj/carousel-quote.png
- /private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design-bjj/carousel-stat.png
- /private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design-bjj/carousel-cta.png
- /private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design-bjj/still.png
- /private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design-bjj/frame_carousel.png

The render tool (a small Go program that assembles the templates, fills sample values, screenshots and checks size and placeholders) lives beside them at `.../scratchpad/design-bjj/render/main.go`; it is scratch, not part of the repo.
