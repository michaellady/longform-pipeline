package statics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// Options configures one render-statics run.
type Options struct {
	Templates Locator
	Chrome    string // "" = ChromePath()
	FFmpeg    string // "" = FFmpegPath()
	Video     string // source for frame extraction (delivery.mp4)
	Brand     model.Brand
	Only      string // render only this format; "" = all
}

// Page is one filled HTML document and the PNG it renders to.
type Page struct {
	HTML string // path
	PNG  string // path
}

// Result is what one static produced, mirrored into index.json.
type Result struct {
	ID        string              `json:"id"`
	Format    string              `json:"format"`
	Caption   string              `json:"caption"`
	Files     []string            `json:"files"`     // relative to the static's dir
	Platforms map[string][]string `json:"platforms"` // platform -> files to post there
}

// RenderAll renders every static in the draft under <jobDir>/statics/<id>/
// and returns one Result per static rendered. The job's Artifacts.Statics is
// set by the caller.
func RenderAll(jobDir string, d *brief.Draft, opts Options) ([]Result, error) {
	work := filepath.Join(jobDir, "statics", ".work")
	r, err := NewRenderer(opts.Chrome, work)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	var results []Result
	for _, s := range d.Statics {
		if opts.Only != "" && s.Format != opts.Only {
			continue
		}
		res, err := RenderOne(jobDir, s, opts, r)
		if err != nil {
			return results, fmt.Errorf("static %s (%s): %w", s.ID, s.Format, err)
		}
		results = append(results, *res)
	}
	return results, nil
}

// RenderOne renders a single static into <jobDir>/statics/<id>/.
func RenderOne(jobDir string, s brief.Static, opts Options, r *Renderer) (*Result, error) {
	tplPath, err := opts.Templates.Find(s.Format)
	if err != nil {
		return nil, err
	}
	tplBytes, err := os.ReadFile(tplPath)
	if err != nil {
		return nil, err
	}
	tpl := string(tplBytes)
	outDir := filepath.Join(jobDir, "statics", s.ID)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	base := map[string]string{"BRAND": opts.Brand.Display, "SITE": opts.Brand.Site, "CAPTION": s.Caption}
	res := &Result{ID: s.ID, Format: s.Format, Caption: s.Caption, Platforms: map[string][]string{}}

	var pages []Page
	switch s.Format {
	case "quote":
		vars := merge(base, map[string]string{"QUOTE": s.Inputs.Quote, "ATTRIBUTION": s.Inputs.Attribution, "INDEX": "1", "TOTAL": "1"})
		p, err := writePage(outDir, "quote", tpl, vars)
		if err != nil {
			return nil, err
		}
		pages = append(pages, p)
	case "still":
		frame := filepath.Join(outDir, "frame.jpg")
		if err := ExtractFrame(opts.FFmpeg, opts.Video, s.Inputs.TimestampMs, frame); err != nil {
			return nil, err
		}
		vars := merge(base, map[string]string{"FRAME": fileURL(frame), "INDEX": "1", "TOTAL": "1"})
		p, err := writePage(outDir, "still", tpl, vars)
		if err != nil {
			return nil, err
		}
		pages = append(pages, p)
	case "carousel", "frame_carousel":
		if len(s.Inputs.Slides) == 0 {
			return nil, fmt.Errorf("no slides")
		}
		total := fmt.Sprint(len(s.Inputs.Slides))
		for i, sl := range s.Inputs.Slides {
			name := fmt.Sprintf("slide-%02d", i+1)
			vars := merge(base, map[string]string{"SLIDE_KIND": sl.Kind, "TEXT": sl.Text, "SUB": sl.Sub, "INDEX": fmt.Sprint(i + 1), "TOTAL": total, "QUOTE": sl.Text, "ATTRIBUTION": sl.Sub})
			if s.Format == "frame_carousel" {
				frame := filepath.Join(outDir, name+".jpg")
				if err := ExtractFrame(opts.FFmpeg, opts.Video, sl.TimestampMs, frame); err != nil {
					return nil, err
				}
				vars["FRAME"] = fileURL(frame)
			}
			p, err := writePage(outDir, name, tpl, vars)
			if err != nil {
				return nil, err
			}
			pages = append(pages, p)
		}
	default:
		return nil, fmt.Errorf("unknown format %q (known: %s)", s.Format, strings.Join(Formats, ", "))
	}

	var pngs []string
	for _, p := range pages {
		if err := r.Screenshot(p.HTML, p.PNG); err != nil {
			return nil, err
		}
		w, h, err := PNGSize(p.PNG)
		if err != nil {
			return nil, err
		}
		if w != Width || h != Height {
			return nil, fmt.Errorf("%s is %dx%d, want %dx%d", p.PNG, w, h, Width, Height)
		}
		pngs = append(pngs, filepath.Base(p.PNG))
	}
	res.Files = append(res.Files, pngs...)

	pdf := ""
	if len(pages) > 1 {
		pdfHTML := filepath.Join(outDir, "document.html")
		if err := os.WriteFile(pdfHTML, []byte(pdfDocument(pages)), 0o644); err != nil {
			return nil, err
		}
		pdf = s.Format + ".pdf"
		if err := r.PrintPDF(pdfHTML, filepath.Join(outDir, pdf)); err != nil {
			return nil, err
		}
		res.Files = append(res.Files, pdf)
	}
	for _, p := range s.Platforms {
		if pdf != "" && strings.HasPrefix(p, "linkedin") {
			res.Platforms[p] = []string{pdf}
		} else {
			res.Platforms[p] = pngs
		}
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := model.WriteFileAtomic(filepath.Join(outDir, "index.json"), append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	return res, nil
}

func writePage(outDir, name, tpl string, vars map[string]string) (Page, error) {
	filled, err := Fill(tpl, vars)
	if err != nil {
		return Page{}, err
	}
	p := Page{HTML: filepath.Join(outDir, name+".html"), PNG: filepath.Join(outDir, name+".png")}
	return p, os.WriteFile(p.HTML, []byte(filled), 0o644)
}

func merge(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// pdfDocument is one tall HTML with one Width x Height page per rendered PNG,
// which Chrome prints to a multi-page PDF (the LinkedIn document format).
func pdfDocument(pages []Page) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><html><head><meta charset=\"utf-8\"><style>@page{size:%dpx %dpx;margin:0}html,body{margin:0;padding:0}.page{width:%dpx;height:%dpx;page-break-after:always;overflow:hidden}.page:last-child{page-break-after:auto}.page img{display:block;width:%dpx;height:%dpx}</style></head><body>", Width, Height, Width, Height, Width, Height)
	for _, p := range pages {
		fmt.Fprintf(&b, "<div class=\"page\"><img src=\"%s\"></div>", fileURL(p.PNG))
	}
	b.WriteString("</body></html>")
	return b.String()
}
