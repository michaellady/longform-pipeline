package statics

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// The newsletter-carousel engine reuses claude-social-media-skills/
// carousel-newsletter as is: its five two-zone HTML templates, its
// gen_illustration.py (Gemini with the EVC banner as style reference) and its
// render.sh (headless Chrome to a 1080x1350 PNG). This file is transport
// only: it checks the slide structure the skill drafted, fills placeholders
// and calls those scripts. Which scenes and which words is the skill's call.

// ErrCarouselInput marks a draft the engine refuses before spending anything
// (exit 65): wrong slide structure, a missing field, a quote that is not a
// verbatim transcript span, a CTA accent word other than "newsletter".
var ErrCarouselInput = errors.New("carousel input")

// MaxQuoteRunes is carousel-newsletter's quote-slide cap.
const MaxQuoteRunes = 260

// CTAAccentWord is the Comment-to-DM trigger the CTA slide must show.
const CTAAccentWord = "newsletter"

// CarouselTemplates maps a slide kind to its carousel-newsletter template.
var CarouselTemplates = map[string]string{
	"hook":    "01-hook.html",
	"section": "02-section.html",
	"quote":   "03-quote.html",
	"stat":    "04-stat.html",
	"cta":     "05-cta.html",
}

// CarouselAspects is the illustration aspect per slide kind (the shape of
// that template's illustration zone).
var CarouselAspects = map[string]string{
	"hook": "4:3", "section": "9:16", "quote": "16:9", "stat": "9:16", "cta": "9:16",
}

// CarouselStructure is carousel-newsletter Phase 2's fixed 10-slide order;
// slide 8 may be a stat or a section.
var CarouselStructure = [][]string{
	{"hook"}, {"section"}, {"quote"}, {"stat"}, {"quote"},
	{"section"}, {"quote"}, {"stat", "section"}, {"section"}, {"cta"},
}

// autoFields are filled by the engine, never by the draft.
var autoFields = map[string]bool{"ILLUS_IMG": true, "PAGE_CURRENT": true, "PAGE_TOTAL": true}

var carouselPlaceholderRe = regexp.MustCompile(`\{\{\s*([A-Z][A-Z0-9_]*)\s*\}\}`)

// CarouselOptions configures one render-carousel run.
type CarouselOptions struct {
	Dir         string        // statics.carousel_dir (holds templates/)
	Only        string        // render only this static id; "" = every carousel and quote static
	Illustrate  bool          // generate missing illustrations with the generator
	GenInterval time.Duration // pause between generator calls (Gemini rate limit)
	// Generator is the illustration command prefix; it receives
	// "<scene>" <out.png> --aspect <ratio>. Default: python3
	// <Dir>/templates/gen_illustration.py, or $LONGFORM_GEN_ILLUSTRATION.
	Generator []string
	// Render is the HTML->PNG command prefix; it receives <in.html>
	// <out.png>. Default: bash <Dir>/templates/render.sh, or
	// $LONGFORM_CAROUSEL_RENDER.
	Render     []string
	Transcript []string // cleaned transcript words for the verbatim check
	Log        io.Writer
}

// RenderedFile is one slide PNG with the digest a visual review binds to.
type RenderedFile struct {
	Path   string `json:"path"` // job-relative
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// CarouselResult is one static's render, printed as one JSON line.
type CarouselResult struct {
	ID     string         `json:"id"`
	Format string         `json:"format"`
	Files  []RenderedFile `json:"files"`
}

func (o CarouselOptions) templatesDir() string { return filepath.Join(o.Dir, "templates") }

func (o CarouselOptions) generator() []string {
	if len(o.Generator) > 0 {
		return o.Generator
	}
	if g := os.Getenv("LONGFORM_GEN_ILLUSTRATION"); g != "" {
		return []string{g}
	}
	return []string{"python3", filepath.Join(o.templatesDir(), "gen_illustration.py")}
}

func (o CarouselOptions) renderer() []string {
	if len(o.Render) > 0 {
		return o.Render
	}
	if r := os.Getenv("LONGFORM_CAROUSEL_RENDER"); r != "" {
		return []string{r}
	}
	return []string{"bash", filepath.Join(o.templatesDir(), "render.sh")}
}

func (o CarouselOptions) logf(format string, a ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, format, a...)
	}
}

// CarouselStatics returns the statics the engine renders: format carousel or
// quote, filtered by only.
func CarouselStatics(d *brief.Draft, only string) []brief.Static {
	var out []brief.Static
	for _, s := range d.Statics {
		if only != "" && s.ID != only {
			continue
		}
		if s.Format == "carousel" || s.Format == "quote" {
			out = append(out, s)
		}
	}
	return out
}

// IllustrationPath is the slide's job-relative illustration PNG.
func IllustrationPath(s brief.Static, i int) string {
	if p := s.Inputs.Slides[i].Illustration; p != "" {
		return p
	}
	return filepath.ToSlash(filepath.Join("statics", s.ID, "illus", fmt.Sprintf("slide-%02d.png", i+1)))
}

// slideFields is the placeholder map for one slide before the auto fields:
// the draft's fields, with QUOTE defaulting to text and ACCENT_WORD to
// "newsletter".
func slideFields(sl brief.Slide) map[string]string {
	f := map[string]string{}
	for k, v := range sl.Fields {
		f[strings.ToUpper(k)] = v
	}
	if sl.Kind == "quote" {
		if _, ok := f["QUOTE"]; !ok && sl.Text != "" {
			f["QUOTE"] = sl.Text
		}
	}
	if sl.Kind == "cta" {
		if _, ok := f["ACCENT_WORD"]; !ok {
			f["ACCENT_WORD"] = CTAAccentWord
		}
	}
	return f
}

// ValidateCarousel checks one static against the fixed structure, the
// templates' placeholders, the quote rules and the CTA trigger. It returns
// every problem, not just the first, so one draft round fixes them all.
func ValidateCarousel(s brief.Static, templatesDir string, transcript []string) []string {
	var probs []string
	slides := s.Inputs.Slides
	switch s.Format {
	case "carousel":
		if len(slides) != len(CarouselStructure) {
			probs = append(probs, fmt.Sprintf("carousel has %d slides, want %d (hook, section, quote, stat, quote, section, quote, stat|section, section, cta)", len(slides), len(CarouselStructure)))
		}
		for i := 0; i < len(slides) && i < len(CarouselStructure); i++ {
			if !contains(CarouselStructure[i], slides[i].Kind) {
				probs = append(probs, fmt.Sprintf("slide %d is %q, want %s", i+1, slides[i].Kind, strings.Join(CarouselStructure[i], " or ")))
			}
		}
	case "quote":
		if len(slides) != 1 || slides[0].Kind != "quote" {
			probs = append(probs, "a quote static is exactly one slide of kind quote")
		}
	default:
		return []string{fmt.Sprintf("format %q is not a newsletter-carousel format (carousel, quote)", s.Format)}
	}
	for i, sl := range slides {
		n := i + 1
		tplName, ok := CarouselTemplates[sl.Kind]
		if !ok {
			probs = append(probs, fmt.Sprintf("slide %d: unknown kind %q", n, sl.Kind))
			continue
		}
		tpl, err := os.ReadFile(filepath.Join(templatesDir, tplName))
		if err != nil {
			probs = append(probs, fmt.Sprintf("slide %d: template: %v", n, err))
			continue
		}
		fields := slideFields(sl)
		var missing []string
		for _, key := range placeholders(string(tpl)) {
			if _, ok := fields[key]; !ok && !autoFields[key] {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			probs = append(probs, fmt.Sprintf("slide %d (%s): missing fields %s", n, sl.Kind, strings.Join(missing, ", ")))
		}
		if strings.TrimSpace(sl.Scene) == "" && sl.Illustration == "" {
			probs = append(probs, fmt.Sprintf("slide %d (%s): scene is empty (the illustration prompt)", n, sl.Kind))
		}
		switch sl.Kind {
		case "quote":
			q := fields["QUOTE"]
			if strings.TrimSpace(q) == "" {
				break
			}
			if r := utf8.RuneCountInString(q); r > MaxQuoteRunes {
				probs = append(probs, fmt.Sprintf("slide %d: quote is %d characters, cap %d", n, r, MaxQuoteRunes))
			}
			if !VerbatimIn(q, transcript) {
				probs = append(probs, fmt.Sprintf("slide %d: quote is not a verbatim transcript span: %q", n, q))
			}
		case "cta":
			if !strings.EqualFold(strings.TrimSpace(fields["ACCENT_WORD"]), CTAAccentWord) {
				probs = append(probs, fmt.Sprintf("slide %d: CTA accent word is %q, must be %q (the Comment-to-DM trigger)", n, fields["ACCENT_WORD"], CTAAccentWord))
			}
		}
	}
	return probs
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// placeholders lists the distinct {{KEY}} tokens in tpl, sorted.
func placeholders(tpl string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range carouselPlaceholderRe.FindAllStringSubmatch(tpl, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// FillCarousel substitutes every placeholder: field values HTML-escaped,
// ILLUS_IMG as the file URL the engine built. A token with no value is an
// error so a slide never renders with literal braces.
func FillCarousel(tpl string, fields map[string]string) (string, error) {
	var missing []string
	out := carouselPlaceholderRe.ReplaceAllStringFunc(tpl, func(m string) string {
		key := carouselPlaceholderRe.FindStringSubmatch(m)[1]
		v, ok := fields[key]
		if !ok {
			missing = append(missing, key)
			return m
		}
		if key == "ILLUS_IMG" {
			return v
		}
		return html.EscapeString(v)
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("%w: unfilled placeholders %s", ErrCarouselInput, strings.Join(missing, ", "))
	}
	return out, nil
}

// CleanWords lowercases and keeps letters and digits per word, dropping
// words left empty (a bare dash), the way the censor matches transcript
// words.
func CleanWords(text string) []string {
	var out []string
	for _, w := range strings.Fields(text) {
		var b strings.Builder
		for _, r := range strings.ToLower(w) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			out = append(out, b.String())
		}
	}
	return out
}

// VerbatimIn reports whether quote's cleaned words occur as one contiguous
// run in the cleaned transcript words. Case and punctuation are ignored
// (whisper's punctuation is a guess); words, their order and their
// adjacency are not.
func VerbatimIn(quote string, transcript []string) bool {
	q := CleanWords(quote)
	if len(q) == 0 || len(q) > len(transcript) {
		return false
	}
outer:
	for i := 0; i+len(q) <= len(transcript); i++ {
		for j := range q {
			if transcript[i+j] != q[j] {
				continue outer
			}
		}
		return true
	}
	return false
}

// LoadTranscriptWords reads <jobDir>/analysis/transcript.json and returns
// its cleaned words.
func LoadTranscriptWords(jobDir string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(jobDir, "analysis", "transcript.json"))
	if err != nil {
		return nil, err
	}
	var t struct {
		Words []struct {
			W string `json:"w"`
		} `json:"words"`
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("analysis/transcript.json: %w", err)
	}
	var parts []string
	for _, w := range t.Words {
		parts = append(parts, w.W)
	}
	return CleanWords(strings.Join(parts, " ")), nil
}

// RenderCarousels validates every carousel and quote static first (nothing
// is generated or rendered when any is invalid), then makes each one's
// missing illustrations (only with Illustrate) and renders its slides into
// <jobDir>/statics/<id>/slide-NN.png, writing index.json like render-statics.
func RenderCarousels(jobDir string, d *brief.Draft, opts CarouselOptions) ([]CarouselResult, error) {
	targets := CarouselStatics(d, opts.Only)
	if len(targets) == 0 {
		if opts.Only != "" {
			return nil, fmt.Errorf("%w: no carousel or quote static with id %q", ErrCarouselInput, opts.Only)
		}
		return nil, nil
	}
	var probs []string
	for _, s := range targets {
		for _, p := range ValidateCarousel(s, opts.templatesDir(), opts.Transcript) {
			probs = append(probs, fmt.Sprintf("static %s: %s", s.ID, p))
		}
	}
	if len(probs) > 0 {
		return nil, fmt.Errorf("%w:\n  %s", ErrCarouselInput, strings.Join(probs, "\n  "))
	}

	generated := 0
	var results []CarouselResult
	for _, s := range targets {
		outDir := filepath.Join(jobDir, "statics", s.ID)
		if err := os.MkdirAll(filepath.Join(outDir, "html"), 0o755); err != nil {
			return results, err
		}
		// Missing illustrations first, so a generator failure leaves the
		// previous render in place.
		for i, sl := range s.Inputs.Slides {
			illus := filepath.Join(jobDir, filepath.FromSlash(IllustrationPath(s, i)))
			if _, err := os.Stat(illus); err == nil {
				continue
			}
			if !opts.Illustrate {
				return results, fmt.Errorf("%w: static %s slide %d: illustration %s is missing; pass --illustrate to generate it", ErrCarouselInput, s.ID, i+1, IllustrationPath(s, i))
			}
			if generated > 0 && opts.GenInterval > 0 {
				time.Sleep(opts.GenInterval)
			}
			if err := os.MkdirAll(filepath.Dir(illus), 0o755); err != nil {
				return results, err
			}
			args := append(append([]string{}, opts.generator()...), sl.Scene, illus, "--aspect", CarouselAspects[sl.Kind])
			opts.logf("render-carousel: %s slide %d: generating illustration (%s)\n", s.ID, i+1, CarouselAspects[sl.Kind])
			if err := run(args); err != nil {
				return results, fmt.Errorf("static %s slide %d: illustration: %w", s.ID, i+1, err)
			}
			generated++
			if _, err := os.Stat(illus); err != nil {
				return results, fmt.Errorf("static %s slide %d: generator wrote no %s", s.ID, i+1, illus)
			}
		}
		// Stale slides from an earlier, longer render must not be posted.
		old, _ := filepath.Glob(filepath.Join(outDir, "slide-*.png"))
		for _, f := range old {
			os.Remove(f)
		}
		res := CarouselResult{ID: s.ID, Format: s.Format}
		var pngs []string
		total := fmt.Sprint(len(s.Inputs.Slides))
		for i, sl := range s.Inputs.Slides {
			tpl, err := os.ReadFile(filepath.Join(opts.templatesDir(), CarouselTemplates[sl.Kind]))
			if err != nil {
				return results, err
			}
			fields := slideFields(sl)
			illus, err := filepath.Abs(filepath.Join(jobDir, filepath.FromSlash(IllustrationPath(s, i))))
			if err != nil {
				return results, err
			}
			fields["ILLUS_IMG"] = (&url.URL{Scheme: "file", Path: illus}).String()
			fields["PAGE_CURRENT"] = fmt.Sprint(i + 1)
			fields["PAGE_TOTAL"] = total
			filled, err := FillCarousel(string(tpl), fields)
			if err != nil {
				return results, fmt.Errorf("static %s slide %d: %w", s.ID, i+1, err)
			}
			name := fmt.Sprintf("slide-%02d", i+1)
			htmlPath := filepath.Join(outDir, "html", name+".html")
			if err := os.WriteFile(htmlPath, []byte(filled), 0o644); err != nil {
				return results, err
			}
			pngPath := filepath.Join(outDir, name+".png")
			if err := run(append(append([]string{}, opts.renderer()...), htmlPath, pngPath)); err != nil {
				return results, fmt.Errorf("static %s slide %d: render: %w", s.ID, i+1, err)
			}
			w, h, err := PNGSize(pngPath)
			if err != nil {
				return results, fmt.Errorf("static %s slide %d: %w", s.ID, i+1, err)
			}
			if w != Width || h != Height {
				return results, fmt.Errorf("static %s slide %d: %s is %dx%d, want %dx%d", s.ID, i+1, pngPath, w, h, Width, Height)
			}
			sum, err := FileSHA256(pngPath)
			if err != nil {
				return results, err
			}
			res.Files = append(res.Files, RenderedFile{Path: filepath.ToSlash(filepath.Join("statics", s.ID, name+".png")), SHA256: sum, Width: w, Height: h})
			pngs = append(pngs, name+".png")
		}
		ix := Result{ID: s.ID, Format: s.Format, Caption: s.Caption, Files: pngs, Platforms: map[string][]string{}}
		for _, p := range s.Platforms {
			ix.Platforms[p] = pngs // every channel gets every slide, as carousel-newsletter posts them
		}
		b, err := json.MarshalIndent(ix, "", "  ")
		if err != nil {
			return results, err
		}
		if err := model.WriteFileAtomic(filepath.Join(outDir, "index.json"), append(b, '\n'), 0o644); err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

func run(args []string) error {
	cmd := exec.Command(args[0], args[1:]...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %v: %s", filepath.Base(args[0]), err, tail(strings.TrimSpace(out.String()), 600))
	}
	return nil
}

// FileSHA256 is the hex sha256 of a file's bytes.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
