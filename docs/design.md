# Static post design (D17)

The four static formats are designed once per brand in Claude Design and rendered deterministically at run time by `longform brief render-statics` (headless Chrome, 1080x1350). This page records the EVC brand's artifacts, the template placeholder contract, and the verification run.

## Artifacts

| Artifact | URL |
| --- | --- |
| Design System "Enterprise Vibe Code" (tokens for light and dark, type scale, spacing, radius; components QuoteCard, SlideHeader, CaptionBand, CTABlock; cover; the EVC mark as an asset) | https://claude.ai/artifact/VRsio6cnhUn1LUqpGZSNXT |
| Design "EVC static post templates" (eight 1080x1350 artboards: quote card, frame still, frame-carousel panel, and the five carousel kinds) | https://claude.ai/artifact/D86kDe1MLuG4gx8Cz2kAU7 |

Both are private until shared from the page's Share menu. The artboards and the exported templates are the same markup and CSS; the artboards carry sample copy where the templates carry placeholders.

## Exported templates

Profile templates directory: `~/.config/longform-pipeline/profiles/evc/templates/`

| File | Format | Placeholders |
| --- | --- | --- |
| `quote.html` | quote card | `{{QUOTE}}`, `{{ATTRIBUTION}}`, `{{BRAND}}`, `{{SITE}}` |
| `carousel.html` | transcript carousel slide | `{{SLIDE_KIND}}`, `{{TEXT}}`, `{{SUB}}`, `{{INDEX}}`, `{{TOTAL}}`, `{{BRAND}}`, `{{SITE}}` |
| `still.html` | frame still | `{{FRAME}}`, `{{CAPTION}}`, `{{BRAND}}`, `{{SITE}}` |
| `frame_carousel.html` | frame-carousel panel | `{{FRAME}}`, `{{CAPTION}}`, `{{INDEX}}`, `{{TOTAL}}`, `{{BRAND}}`, `{{SITE}}` |

### Placeholder contract

Every placeholder is a literal `{{NAME}}` token replaced by plain string substitution; the renderer HTML-escapes values before substitution. No other `{{ }}` tokens exist in the files (`grep -o '{{[A-Z_]*}}' *.html | sort -u` lists only the names below).

| Placeholder | Meaning | Notes |
| --- | --- | --- |
| `{{QUOTE}}` | the verbatim quote | no surrounding quotation marks; the template draws the mark |
| `{{ATTRIBUTION}}` | who said it | the template prefixes an em dash |
| `{{BRAND}}` | the brand name | rendered uppercase, tracked, beside the EVC mark |
| `{{SITE}}` | the site URL as shown | e.g. `enterprisevibecode.com`; hidden on the hook slide |
| `{{FRAME}}` | an image URL for the video frame | used in `<img src>`; `file://` paths work in headless Chrome; cropped with `object-fit: cover`, centered |
| `{{CAPTION}}` | the spoken caption for a frame | one sentence; shrinks in 2px steps to 30px if it overflows |
| `{{SLIDE_KIND}}` | a class on the carousel's root element | exactly one of `hook`, `section`, `quote`, `stat`, `cta` |
| `{{TEXT}}` | the slide's main line | hook: uppercase headline; section: headline; quote: the quote; stat: the number; cta: the headline |
| `{{SUB}}` | the slide's second line | hook: tease; section: body; quote: speaker (em dash added); stat: label; cta: the comment trigger, drawn as the green button |
| `{{INDEX}}`, `{{TOTAL}}` | slide counter | drawn as `INDEX / TOTAL` in the top-right pill |

Each template carries a small inline script that shrinks `[data-fit]` text in 2px steps until it fits its box (floors: 40px for quotes and headlines, 30px for captions). It runs at load and again when fonts are ready, so headless Chrome's screenshot reflects it.

## Fonts

Inter, weights 500 to 900, loaded from Google Fonts in each template's `<head>`:

`https://fonts.googleapis.com/css2?family=Inter:wght@500;600;700;800;900&display=swap`

Fallback stack: `"Inter", "Helvetica Neue", Helvetica, Arial, sans-serif`. Rendering needs network access for the font files; offline, Helvetica Neue renders with the same layout and slightly different metrics. No font files are bundled.

## Verification

Each template was filled with sample values via `sed` into a temporary copy and rendered with

```
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --disable-gpu --hide-scrollbars \
  --window-size=1080,1350 --screenshot=<out>.png file://<filled copy>
```

All four PNGs measured 1080x1350 with `sips -g pixelWidth -g pixelHeight` and were viewed. Screenshots (session scratchpad, not committed):

- `/private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design/quote.png`
- `/private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design/carousel.png` (kind `hook`)
- `/private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design/still.png`
- `/private/tmp/claude-501/-Users-mikelady-dev/9e068925-f827-4535-b0d0-a77ecc2eaa4c/scratchpad/design/frame_carousel.png`

The sample frame for the still and frame-carousel renders was a JPEG made from `~/Pictures/evc_banner2.png`; a real run substitutes an ffmpeg frame.

## Brand inputs

- Carousel tokens and slide templates: `~/dev/claude-social-media-skills/carousel-newsletter/templates/` (`shared.css`, `01-hook.html` to `05-cta.html`, the EVC mark in `illustrations.svg`).
- Banner art: `~/Pictures/evc_banner.png`, `~/Pictures/evc_banner2.png` (cream ground, faint grid, navy and green wordmark, Lego-brick motif).
- Site copy: https://www.enterprisevibecode.com ("AI makes code cheap. We make it reliable.").
