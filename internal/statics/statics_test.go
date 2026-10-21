package statics

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/model"
)

func defaultDir(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	d := filepath.Join(wd, "..", "..", "plugins", "longform-pipeline", "templates", "default")
	if _, err := os.Stat(d); err != nil {
		t.Fatalf("default templates missing: %v", err)
	}
	return d
}

var leftover = regexp.MustCompile(`\{\{[^}]*\}\}`)

func TestFillDefaultTemplatesLeaveNoTokens(t *testing.T) {
	dir := defaultDir(t)
	vars := map[string]string{"QUOTE": "Ship <it> & \"mean\" it", "ATTRIBUTION": "Mike", "BRAND": "Brand", "SITE": "https://example.com", "FRAME": "file:///tmp/frame.jpg", "CAPTION": "A caption", "SLIDE_KIND": "hook", "TEXT": "Text", "SUB": "Sub", "INDEX": "1", "TOTAL": "3"}
	for _, f := range Formats {
		b, err := os.ReadFile(filepath.Join(dir, f+".html"))
		if err != nil {
			t.Fatal(err)
		}
		out, err := Fill(string(b), vars)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if m := leftover.FindString(out); m != "" {
			t.Errorf("%s: leftover token %q", f, m)
		}
		if f == "quote" && !strings.Contains(out, "Ship &lt;it&gt; &amp; &#34;mean&#34; it") {
			t.Errorf("quote not escaped:\n%s", out)
		}
		if f == "still" && !strings.Contains(out, `src="file:///tmp/frame.jpg"`) {
			t.Errorf("FRAME must be inserted raw")
		}
		if f == "carousel" && !strings.Contains(out, `class="kind-hook"`) {
			t.Errorf("carousel kind class missing")
		}
	}
}

func TestFillRejectsUnknownPlaceholder(t *testing.T) {
	_, err := Fill("<p>{{QUOTE}} {{BOGUS}} {{ OTHER }}</p>", map[string]string{"QUOTE": "q"})
	if err == nil || !strings.Contains(err.Error(), "BOGUS") || !strings.Contains(err.Error(), "OTHER") {
		t.Fatalf("err = %v", err)
	}
	out, err := Fill("<p>{{QUOTE}} {{ TOTAL }}</p>", map[string]string{"QUOTE": "q"})
	if err != nil || out != "<p>q </p>" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestLocatorOrder(t *testing.T) {
	prof := t.TempDir()
	os.WriteFile(filepath.Join(prof, "quote.html"), []byte("x"), 0o644)
	l := Locator{Dirs: []string{"", prof, defaultDir(t)}}
	if p, err := l.Find("quote"); err != nil || p != filepath.Join(prof, "quote.html") {
		t.Fatalf("profile dir should win: %s %v", p, err)
	}
	if p, err := l.Find("still"); err != nil || !strings.HasSuffix(p, "default/still.html") {
		t.Fatalf("fallback: %s %v", p, err)
	}
	if _, err := l.Find("nope"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("missing format: %v", err)
	}
}

// TestIntegrationRenderQuote drives real headless Chrome. Run with
// LONGFORM_INTEGRATION=1 go test ./internal/statics -run Integration -v
func TestIntegrationRenderQuote(t *testing.T) {
	if os.Getenv("LONGFORM_INTEGRATION") != "1" {
		t.Skip("set LONGFORM_INTEGRATION=1 to render with Chrome")
	}
	jobDir := t.TempDir()
	d := &brief.Draft{SchemaVersion: "1", Statics: []brief.Static{
		{ID: "s1", Format: "quote", Platforms: []string{"instagram", "linkedin_page"}, Caption: "cap", Inputs: brief.StaticInputs{Quote: "The walk is the work. Record it once, ship it everywhere.", Attribution: "Mike Lady"}},
		{ID: "s2", Format: "carousel", Platforms: []string{"instagram", "linkedin_page"}, Caption: "cap", Inputs: brief.StaticInputs{Slides: []brief.Slide{{Kind: "hook", Text: "One take. Six surfaces."}, {Kind: "stat", Text: "25", Sub: "days of backlog from one walk"}, {Kind: "cta", Text: "Subscribe for the next one", Sub: "example.com/subscribe"}}}},
	}}
	opts := Options{Templates: Locator{Dirs: []string{defaultDir(t)}}, Brand: model.Brand{Display: "Brand", Site: "example.com"}}
	results, err := RenderAll(jobDir, d, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	for _, r := range results {
		for _, f := range r.Files {
			p := filepath.Join(jobDir, "statics", r.ID, f)
			st, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(f, ".png") {
				w, h, err := PNGSize(p)
				if err != nil {
					t.Fatal(err)
				}
				if w != Width || h != Height {
					t.Errorf("%s: %dx%d", f, w, h)
				}
				t.Logf("%s/%s %dx%d %d bytes", r.ID, f, w, h, st.Size())
			} else {
				t.Logf("%s/%s %d bytes", r.ID, f, st.Size())
			}
		}
		if _, err := os.Stat(filepath.Join(jobDir, "statics", r.ID, "index.json")); err != nil {
			t.Fatal(err)
		}
	}
	if got := results[1].Platforms["linkedin_page"]; len(got) != 1 || got[0] != "carousel.pdf" {
		t.Errorf("linkedin should get the PDF: %v", got)
	}
	if got := results[1].Platforms["instagram"]; len(got) != 3 {
		t.Errorf("instagram should get 3 slides: %v", got)
	}
	if keep := os.Getenv("LONGFORM_KEEP"); keep != "" {
		os.CopyFS(keep, os.DirFS(jobDir))
		t.Logf("kept output under %s", keep)
	}
}
