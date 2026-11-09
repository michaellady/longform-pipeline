package newsletter

import (
	"os"
	"path/filepath"
	"testing"
)

// The real script's body, so the test pins the exact string the newsletter
// skills post.
const ctaScript = `#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 1 ] || [ -z "$1" ]; then echo "usage" >&2; exit 64; fi
printf 'Comment "newsletter" to get my latest post, "%s"' "$1"
`

func TestLatestAndCTA(t *testing.T) {
	dir := t.TempDir()
	corpus := filepath.Join(dir, "cache.json")
	os.WriteFile(corpus, []byte(`{"posts":[
	  {"title":"Older Issue","published_at":"2026-10-25","source_type":"newsletter"},
	  {"title":"A Live Stream","published_at":"2026-11-09","source_type":"youtube_live"},
	  {"title":"Chasing Cars","published_at":"2026-11-08","source_type":"newsletter"},
	  {"title":"Same Day Later Listed","published_at":"2026-11-08","source_type":"newsletter"},
	  {"title":"Undated","published_at":"","source_type":"newsletter"}]}`), 0o644)
	p, err := Latest(corpus)
	if err != nil || p.Title != "Chasing Cars" {
		t.Fatalf("Latest = %+v, %v", p, err)
	}
	script := filepath.Join(dir, "cta.sh")
	os.WriteFile(script, []byte(ctaScript), 0o755)
	cta, post, err := CanonicalCTA(corpus, script)
	if err != nil || post.Title != "Chasing Cars" || cta != `Comment "newsletter" to get my latest post, "Chasing Cars"` {
		t.Fatalf("CanonicalCTA = %q %+v %v", cta, post, err)
	}
	if !EndsWithCTA("A quote from the talk.\n\n"+cta+"\n", cta) || EndsWithCTA(cta+".", cta) || EndsWithCTA("anything", "") {
		t.Fatal("EndsWithCTA")
	}

	os.WriteFile(corpus, []byte(`{"posts":[{"title":"Live","published_at":"2026-11-09","source_type":"youtube_live"}]}`), 0o644)
	if _, err := Latest(corpus); err == nil {
		t.Fatal("a corpus without newsletters must fail")
	}
	if _, err := CTA(script, ""); err == nil {
		t.Fatal("cta.sh refuses an empty title")
	}
}
