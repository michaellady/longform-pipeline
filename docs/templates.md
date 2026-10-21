# Static templates

`longform brief render-statics` turns each entry of `draft.json`'s `statics[]`
into 1080x1350 PNGs (and a PDF for carousels) by filling an HTML template and
screenshotting it with headless Chrome. Frames come from `delivery.mp4` via
ffmpeg at the timestamps the skill chose.

## Where templates live

For each static the renderer looks for `<format>.html` in, first match wins:

1. `profile.statics.templates_dir` (default `~/.config/longform-pipeline/profiles/<profile>/templates/`)
2. `plugins/longform-pipeline/templates/default/` next to the released binary (`<binary dir>/../templates/default`)
3. `plugins/longform-pipeline/templates/default/` under the working directory (for `go run`)

The four formats: `quote`, `carousel`, `still`, `frame_carousel`. The default
templates are brand-neutral, use system fonts and inline CSS, and are meant to
be replaced per brand.

## Placeholders

Every `{{NAME}}` in a template is replaced; values are HTML-escaped except
`FRAME`, which is a URL. A template that uses a token outside this list fails
the render with an error naming the token, so a broken export never ships a
card with literal braces.

| Placeholder | Filled from | Formats |
|---|---|---|
| `{{BRAND}}` | `profile.brand.display` | all |
| `{{SITE}}` | `profile.brand.site` | all |
| `{{CAPTION}}` | `statics[].caption` | all (the still shows it in the caption band) |
| `{{QUOTE}}` | `inputs.quote` (carousels: the slide's `text`) | quote, carousel |
| `{{ATTRIBUTION}}` | `inputs.attribution` (carousels: the slide's `sub`) | quote, carousel |
| `{{FRAME}}` | `file://` URL of the ffmpeg frame at `inputs.timestamp_ms` / the slide's `timestamp_ms` | still, frame_carousel |
| `{{SLIDE_KIND}}` | slide `kind`: `hook`, `section`, `quote`, `stat`, `cta` | carousel, frame_carousel |
| `{{TEXT}}` | slide `text` | carousel, frame_carousel |
| `{{SUB}}` | slide `sub` | carousel, frame_carousel |
| `{{INDEX}}` / `{{TOTAL}}` | 1-based slide number and slide count (`1` / `1` for single cards) | all |

One carousel template renders every slide kind: the default sets
`<body class="kind-{{SLIDE_KIND}}">` and styles each kind in CSS. Do the same
in a Claude Design export so hook, quote, stat and CTA artboards collapse into
one file, or branch on the class however the design needs.

## Output

```
<job>/statics/<id>/
  quote.png | still.png | slide-01.png … slide-NN.png
  carousel.pdf | frame_carousel.pdf        (carousels only; one 1080x1350 page per slide)
  frame.jpg | slide-NN.jpg                  (extracted frames)
  *.html                                    (the filled pages, kept for debugging)
  index.json
```

`index.json` lists `files` and `platforms` (platform -> files to post there):
`linkedin_*` platforms get the PDF for carousels (LinkedIn document post),
everything else gets the PNGs (Instagram carousel, TikTok photo post). The
brief embeds the PNGs once they exist; before that it shows the planned
content.

## Rendering mechanics

Chrome: `--headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=1 --window-size=1080,1450 --screenshot=<raw> file://<page>`
with a private `--user-data-dir` so a running desktop Chrome is never joined.
The new headless viewport is about 87px shorter than the window, so the window
is oversized and the screenshot cropped to 1080x1350 from the top-left in Go.
PDFs: one tall HTML of the rendered PNGs with `@page { size: 1080px 1350px }`
and `--print-to-pdf --no-pdf-header-footer`.
Frames: `ffmpeg -y -ss <s.mmm> -i delivery.mp4 -frames:v 1 -q:v 2 <out.jpg>`.

Overrides: `LONGFORM_CHROME` (binary path; default
`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`) and
`LONGFORM_FFMPEG` (default `ffmpeg` on PATH).

## Replacing the defaults with Claude Design exports (plan D17, section 6)

Design once per brand:

1. Create a Design System artifact for the brand (tokens, type scale, spacing).
2. Create a Design artifact with four artboards at 1080x1350: quote card,
   carousel slide (with hook / section / quote / stat / cta variants), frame
   still with a caption band, frame-carousel panel with caption.
3. Export each artboard's HTML to
   `~/.config/longform-pipeline/profiles/<profile>/templates/<format>.html`.
4. Replace the sample text with the placeholders above. Keep every asset
   inline (CSS, SVG, base64 images): the page is loaded from `file://` with no
   network, and cross-document `<use href="sprite.svg#id">` does not resolve.
5. Set `html, body { width: 1080px; height: 1350px; overflow: hidden }` so the
   crop is exact, and prefer system fonts or embedded `@font-face` data URLs.
6. Run `longform brief render-statics --job <job> --only quote` on any clipped
   job and open `statics/<id>/*.png` to check the result.

Claude Design is never in the per-video loop; the templates are plain files
the renderer fills deterministically.
