// Package statics fills the per-profile HTML templates (or the plugin's
// brand-neutral defaults) from draft.json and renders them to PNG and PDF with
// headless Chrome. Frames come from ffmpeg. No judgment lives here: which
// quote, which frame and which slides were decided by the skill.
package statics

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Formats the pipeline knows; each has a <format>.html template.
var Formats = []string{"quote", "carousel", "still", "frame_carousel"}

// Placeholders every template may use. Documented in docs/templates.md.
var Placeholders = []string{"QUOTE", "ATTRIBUTION", "BRAND", "SITE", "FRAME", "CAPTION", "SLIDE_KIND", "TEXT", "SUB", "INDEX", "TOTAL"}

var placeholderRe = regexp.MustCompile(`\{\{\s*([A-Z_]+)\s*\}\}`)

// Fill replaces every {{PLACEHOLDER}} in tpl with the HTML-escaped value from
// vars. Keys absent from vars become empty. Tokens outside the known set are
// an error so a mis-exported template fails loudly instead of shipping a
// card with literal braces on it. Raw (unescaped) values are allowed only for
// FRAME, which is a URL the renderer built.
func Fill(tpl string, vars map[string]string) (string, error) {
	var unknown []string
	seen := map[string]bool{}
	out := placeholderRe.ReplaceAllStringFunc(tpl, func(m string) string {
		key := placeholderRe.FindStringSubmatch(m)[1]
		if !known(key) {
			if !seen[key] {
				seen[key] = true
				unknown = append(unknown, key)
			}
			return m
		}
		v := vars[key]
		if key == "FRAME" {
			return v
		}
		return html.EscapeString(v)
	})
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return "", fmt.Errorf("template uses unknown placeholder(s) %s; known: %s", strings.Join(unknown, ", "), strings.Join(Placeholders, ", "))
	}
	return out, nil
}

func known(key string) bool {
	for _, p := range Placeholders {
		if p == key {
			return true
		}
	}
	return false
}

// Locator finds a format's template, first dir wins.
type Locator struct {
	Dirs []string
}

// Find returns the path of <format>.html in the first dir that has it.
func (l Locator) Find(format string) (string, error) {
	var tried []string
	for _, d := range l.Dirs {
		if d == "" {
			continue
		}
		p := filepath.Join(d, format+".html")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		tried = append(tried, p)
	}
	return "", fmt.Errorf("no template for format %q; looked in: %s", format, strings.Join(tried, ", "))
}

// DefaultTemplateDirs lists the fallback locations after the profile's own
// dir: next to the released binary (<exe>/../templates/default, which is
// plugins/longform-pipeline/templates/default for the installed plugin) and
// the repo layout relative to the working directory for `go run`.
func DefaultTemplateDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "..", "templates", "default"))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "plugins", "longform-pipeline", "templates", "default"))
	}
	return dirs
}
