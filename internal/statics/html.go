package statics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// The custom-html engine renders HTML the skill designed per post (from the
// profile's brand tokens). Design is the skill's; this is transport only:
// check the pages exist inside the job, render each with headless Chrome to
// a 1080x1350 PNG and record index.json like the other engines.

// MaxPages is the most images one post carries (Instagram and Threads
// carousels take 20).
const MaxPages = 20

var leftoverTokenRe = regexp.MustCompile(`\{\{\s*[A-Za-z][A-Za-z0-9_]*\s*\}\}`)

// HTMLOptions configures one render-html run.
type HTMLOptions struct {
	Chrome string // "" = ChromePath()
	Only   string // static id; "" = every static with pages
}

// ValidateHTMLPages checks one static's pages: 1..MaxPages, each a .html
// file inside the job directory with no unfilled {{TOKEN}}.
func ValidateHTMLPages(jobDir string, s brief.Static) []string {
	var probs []string
	if len(s.Inputs.Pages) == 0 {
		return []string{"inputs.pages is empty (the skill writes one HTML file per image)"}
	}
	if len(s.Inputs.Pages) > MaxPages {
		probs = append(probs, fmt.Sprintf("%d pages, at most %d", len(s.Inputs.Pages), MaxPages))
	}
	root, _ := filepath.Abs(jobDir)
	for i, p := range s.Inputs.Pages {
		n := i + 1
		if filepath.IsAbs(p) || !strings.HasSuffix(strings.ToLower(p), ".html") {
			probs = append(probs, fmt.Sprintf("page %d: %q must be a job-relative .html path", n, p))
			continue
		}
		abs, _ := filepath.Abs(filepath.Join(jobDir, filepath.FromSlash(p)))
		if rel, err := filepath.Rel(root, abs); err != nil || strings.HasPrefix(rel, "..") {
			probs = append(probs, fmt.Sprintf("page %d: %q is outside the job directory", n, p))
			continue
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			probs = append(probs, fmt.Sprintf("page %d: %v", n, err))
			continue
		}
		if m := leftoverTokenRe.FindString(string(b)); m != "" {
			probs = append(probs, fmt.Sprintf("page %d: unfilled placeholder %s", n, m))
		}
	}
	return probs
}

// HTMLStatics returns the statics that carry pages, filtered by only.
func HTMLStatics(d *brief.Draft, only string) []brief.Static {
	var out []brief.Static
	for _, s := range d.Statics {
		if (only == "" || s.ID == only) && (len(s.Inputs.Pages) > 0 || only != "") {
			out = append(out, s)
		}
	}
	return out
}

// RenderHTMLPages validates every target static first (nothing renders when
// any is invalid), then renders each page to statics/<id>/slide-NN.png.
func RenderHTMLPages(jobDir string, d *brief.Draft, opts HTMLOptions) ([]CarouselResult, error) {
	targets := HTMLStatics(d, opts.Only)
	if len(targets) == 0 {
		if opts.Only != "" {
			return nil, fmt.Errorf("%w: no static with id %q", ErrCarouselInput, opts.Only)
		}
		return nil, nil
	}
	var probs []string
	for _, s := range targets {
		for _, p := range ValidateHTMLPages(jobDir, s) {
			probs = append(probs, fmt.Sprintf("static %s: %s", s.ID, p))
		}
	}
	if len(probs) > 0 {
		return nil, fmt.Errorf("%w:\n  %s", ErrCarouselInput, strings.Join(probs, "\n  "))
	}
	work := filepath.Join(jobDir, "statics", ".work")
	r, err := NewRenderer(opts.Chrome, work)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	var results []CarouselResult
	for _, s := range targets {
		outDir := filepath.Join(jobDir, "statics", s.ID)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return results, err
		}
		old, _ := filepath.Glob(filepath.Join(outDir, "slide-*.png"))
		for _, f := range old {
			os.Remove(f)
		}
		res := CarouselResult{ID: s.ID, Format: s.Format}
		var pngs []string
		for i, p := range s.Inputs.Pages {
			name := fmt.Sprintf("slide-%02d.png", i+1)
			out := filepath.Join(outDir, name)
			if err := r.Screenshot(filepath.Join(jobDir, filepath.FromSlash(p)), out); err != nil {
				return results, fmt.Errorf("static %s page %d: %w", s.ID, i+1, err)
			}
			w, h, err := PNGSize(out)
			if err != nil {
				return results, err
			}
			if w != Width || h != Height {
				return results, fmt.Errorf("static %s page %d: %dx%d, want %dx%d", s.ID, i+1, w, h, Width, Height)
			}
			sum, err := FileSHA256(out)
			if err != nil {
				return results, err
			}
			res.Files = append(res.Files, RenderedFile{Path: filepath.ToSlash(filepath.Join("statics", s.ID, name)), SHA256: sum, Width: w, Height: h})
			pngs = append(pngs, name)
		}
		ix := Result{ID: s.ID, Format: s.Format, Caption: s.Caption, Files: pngs, Platforms: map[string][]string{}}
		for _, p := range s.Platforms {
			ix.Platforms[p] = pngs
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
