package statics

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaellady/longform-pipeline/internal/brief"
)

// fakeChrome writes a PNG of the given size wherever --screenshot= points,
// the way headless Chrome does (taller than the frame; the renderer crops).
func fakeChrome(t *testing.T, w, h int) string {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "shot.png")
	writePNG(t, base, w, h)
	script := filepath.Join(dir, "chrome")
	os.WriteFile(script, []byte("#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in --screenshot=*) cp '"+base+"' \"${a#--screenshot=}\";; esac; done\n"), 0o755)
	return script
}

func TestRenderHTMLPages(t *testing.T) {
	job := t.TempDir()
	os.MkdirAll(filepath.Join(job, "statics", "six", "html"), 0o755)
	for _, n := range []string{"p1", "p2"} {
		os.WriteFile(filepath.Join(job, "statics", "six", "html", n+".html"), []byte("<html><body>"+n+"</body></html>"), 0o644)
	}
	d := &brief.Draft{Statics: []brief.Static{
		{ID: "six", Format: "carousel", Platforms: []string{"instagram", "facebook"}, Caption: "c", Inputs: brief.StaticInputs{Pages: []string{"statics/six/html/p1.html", "statics/six/html/p2.html"}}},
		{ID: "no-pages", Format: "still"},
	}}
	res, err := RenderHTMLPages(job, d, HTMLOptions{Chrome: fakeChrome(t, Width, Height+100)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Files) != 2 || res[0].Files[1].Path != "statics/six/slide-02.png" || res[0].Files[0].Height != Height {
		t.Fatalf("results = %+v", res)
	}
	if imgs := StaticImages(job, "six"); len(imgs) != 2 {
		t.Fatalf("index.json images = %v", imgs)
	}

	bad := []brief.Static{{ID: "b", Inputs: brief.StaticInputs{Pages: []string{"../escape.html", "/abs.html", "statics/six/html/missing.html", "statics/six/html/tok.html"}}}}
	os.WriteFile(filepath.Join(job, "statics", "six", "html", "tok.html"), []byte("<p>{{HEADLINE}}</p>"), 0o644)
	_, err = RenderHTMLPages(job, &brief.Draft{Statics: bad}, HTMLOptions{Chrome: fakeChrome(t, Width, Height+100)})
	if !errors.Is(err, ErrCarouselInput) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"outside the job directory", "must be a job-relative .html path", "no such file", "unfilled placeholder {{HEADLINE}}"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if _, err := RenderHTMLPages(job, &brief.Draft{Statics: []brief.Static{d.Statics[0]}}, HTMLOptions{Chrome: fakeChrome(t, 900, 900)}); err == nil {
		t.Fatal("a screenshot smaller than the frame must fail")
	}
}
