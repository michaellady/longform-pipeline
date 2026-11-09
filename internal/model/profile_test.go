package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfilePrepareEncoder(t *testing.T) {
	t.Setenv("LONGFORM_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(ProfilesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, prepare string) {
		t.Helper()
		body := `{"inbox":"/tmp/in"` + prepare + `}`
		if err := os.WriteFile(filepath.Join(ProfilesDir(), name+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("absent", "")
	p, err := LoadProfile("absent")
	if err != nil {
		t.Fatal(err)
	}
	if p.Prepare.Encoder != PrepareEncoderAuto {
		t.Errorf("default prepare.encoder = %q, want auto", p.Prepare.Encoder)
	}
	for _, v := range []string{PrepareEncoderAuto, PrepareEncoderVideoToolbox, PrepareEncoderX264} {
		write("ok", `,"prepare":{"encoder":"`+v+`"}`)
		if p, err := LoadProfile("ok"); err != nil || p.Prepare.Encoder != v {
			t.Errorf("prepare.encoder %q: %v %+v", v, err, p)
		}
	}
	write("bad", `,"prepare":{"encoder":"nvenc"}`)
	if _, err := LoadProfile("bad"); err == nil || !strings.Contains(err.Error(), "prepare.encoder") {
		t.Errorf("unknown encoder must be rejected, got %v", err)
	}
}

func TestProfileNewsletterCarouselAndStaticCTA(t *testing.T) {
	t.Setenv("LONGFORM_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(ProfilesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	load := func(extra string) (*Profile, error) {
		t.Helper()
		body := `{"inbox":"/tmp/in"` + extra + `}`
		if err := os.WriteFile(filepath.Join(ProfilesDir(), "p.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return LoadProfile("p")
	}
	p, err := load(`,"statics":{"engine":"newsletter-carousel","carousel_dir":"/skills/carousel-newsletter","formats":["carousel","quote"]},` +
		`"copy":{"voice_corpus":"/v.json"},"cta":{"static_cta":"newsletter-canon"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.NewsletterCarousel() || p.CTAScriptPath() != "/skills/_shared/cta.sh" {
		t.Errorf("engine=%v cta script=%q", p.NewsletterCarousel(), p.CTAScriptPath())
	}
	if p, err := load(`,"cta":{"static_cta":"newsletter-canon","cta_script":"/x/cta.sh"},"copy":{"voice_corpus":"/v.json"}`); err != nil || p.CTAScriptPath() != "/x/cta.sh" || p.NewsletterCarousel() {
		t.Errorf("explicit cta_script with the default engine: %v %+v", err, p)
	}
	if p, err := load(`,"brand":{"display":"EVC","palette":{"navy":"#14386B"},"fonts":["Inter"],"logo_text":"EVC"},"statics":{"engine":"custom-html","formats":["six_stops","before_after"]}`); err != nil || !p.CustomHTML() || p.Brand.Palette["navy"] != "#14386B" {
		t.Errorf("custom-html: %v %+v", err, p)
	}
	for extra, want := range map[string]string{
		`,"statics":{"engine":"newsletter-carousel","formats":["carousel"]}`:                  "carousel_dir is required",
		`,"statics":{"engine":"newsletter-carousel","carousel_dir":"/c","formats":["still"]}`: `"still" is not a newsletter-carousel format`,
		`,"statics":{"engine":"canva"}`:                                                       "statics.engine",
		`,"cta":{"static_cta":"newsletter-canon","cta_script":"/x/cta.sh"}`:                   "needs copy.voice_corpus",
		`,"cta":{"static_cta":"newsletter-canon"},"copy":{"voice_corpus":"/v.json"}`:          "needs cta.cta_script or statics.carousel_dir",
		`,"cta":{"static_cta":"always"}`:                                                      "cta.static_cta",
	} {
		if _, err := load(extra); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", extra, err, want)
		}
	}
}
