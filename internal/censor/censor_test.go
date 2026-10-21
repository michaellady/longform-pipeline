package censor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaellady/longform-pipeline/internal/media"
	"github.com/michaellady/longform-pipeline/internal/transcribe"
)

func repoList(t *testing.T) *List {
	t.Helper()
	p := filepath.Join("..", "..", "plugins", "longform-pipeline", "censor", "default.json")
	l, err := LoadList(p)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestDefaultListShape(t *testing.T) {
	l := repoList(t)
	if l.SchemaVersion != "1" || len(l.Words) < 10 {
		t.Errorf("list: version=%q words=%d", l.SchemaVersion, len(l.Words))
	}
	v := Vocabulary(l.Words, nil)
	for _, strong := range []string{"fuck", "fucking", "motherfucker", "cunt"} {
		if !v[strong] {
			t.Errorf("%q missing from strong list", strong)
		}
	}
	for _, moderate := range []string{"damn", "hell", "crap", "shit", "bitch", "ass"} {
		if v[moderate] {
			t.Errorf("%q is moderate and must not be in the default list", moderate)
		}
	}
	for _, w := range l.Words {
		if transcribe.CleanWord(w) != w {
			t.Errorf("list entry %q is not in cleaned form", w)
		}
	}
}

func TestMatchAndMerge(t *testing.T) {
	vocab := Vocabulary([]string{"fuck", "fucking"}, []string{"Brand-Secret"})
	words := []transcribe.Word{
		{W: "Hello", StartMs: 0, EndMs: 300},
		{W: "Fucking,", StartMs: 300, EndMs: 700},      // -> 260..740
		{W: "fuck", StartMs: 720, EndMs: 900},          // -> 680..940 overlaps previous
		{W: "f*ck", StartMs: 1000, EndMs: 1200},        // cleaned "fck", not listed
		{W: "brandsecret", StartMs: 2000, EndMs: 2300}, // extra, cleaned form matches
		{W: "FUCK!", StartMs: 9980, EndMs: 10000},      // clamp to duration
		{W: "", StartMs: 10, EndMs: 20},
	}
	spans := Match(words, vocab, DefaultPadMs, 10000)
	if len(spans) != 4 {
		t.Fatalf("matches = %+v", spans)
	}
	if spans[0] != (Span{"fucking", 260, 740}) || spans[1] != (Span{"fuck", 680, 940}) {
		t.Errorf("padding: %+v", spans[:2])
	}
	if spans[3] != (Span{"fuck", 9940, 10000}) {
		t.Errorf("clamp: %+v", spans[3])
	}
	merged := Merge(spans)
	if len(merged) != 3 {
		t.Fatalf("merged = %+v", merged)
	}
	if merged[0] != (Span{"fucking fuck", 260, 940}) {
		t.Errorf("merged[0] = %+v", merged[0])
	}
	if merged[1] != (Span{"brandsecret", 1960, 2340}) {
		t.Errorf("merged[1] = %+v", merged[1])
	}

	// Start-of-file clamp.
	early := Match([]transcribe.Word{{W: "fuck", StartMs: 10, EndMs: 100}}, vocab, DefaultPadMs, 0)
	if early[0].StartMs != 0 || early[0].EndMs != 140 {
		t.Errorf("start clamp: %+v", early)
	}
	// Merge is order-independent and tolerates touching spans.
	m := Merge([]Span{{"b", 500, 600}, {"a", 100, 200}, {"c", 200, 300}, {"a", 150, 180}})
	if len(m) != 2 || m[0] != (Span{"a c", 100, 300}) || m[1] != (Span{"b", 500, 600}) {
		t.Errorf("merge: %+v", m)
	}
	if got := Merge(nil); got == nil || len(got) != 0 {
		t.Errorf("Merge(nil) must be empty non-nil: %#v", got)
	}
}

func TestFilterGraph(t *testing.T) {
	af := media.AudioFormat{SampleRate: 48000, Channels: 2, Layout: "stereo"}
	spans := []Span{{"fuck", 500, 1000}, {"cunt", 2000, 2300}}

	beep := FilterGraph(spans, ModeBeep, af)
	want := "[0:a]volume=enable='between(t,0.500,1.000)':volume=0,volume=enable='between(t,2.000,2.300)':volume=0,aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo[m]" +
		";sine=frequency=1000:sample_rate=48000:duration=0.500,volume=2,afade=t=in:st=0:d=0.030,afade=t=out:st=0.470:d=0.030,pan=stereo|c0=c0|c1=c0,adelay=delays=500:all=1,aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo[b0]" +
		";sine=frequency=1000:sample_rate=48000:duration=0.300,volume=2,afade=t=in:st=0:d=0.030,afade=t=out:st=0.270:d=0.030,pan=stereo|c0=c0|c1=c0,adelay=delays=2000:all=1,aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo[b1]" +
		";[m][b0][b1]amix=inputs=3:duration=first:normalize=0[aout]"
	if beep != want {
		t.Errorf("beep graph:\n got %s\nwant %s", beep, want)
	}

	mute := FilterGraph(spans, ModeMute, af)
	if mute != "[0:a]volume=enable='between(t,0.500,1.000)':volume=0,volume=enable='between(t,2.000,2.300)':volume=0,anull[aout]" {
		t.Errorf("mute graph: %s", mute)
	}
	if strings.Contains(mute, "sine") {
		t.Error("mute must not synthesise a tone")
	}
	if FilterGraph(nil, ModeBeep, af) != "" {
		t.Error("no spans must yield an empty graph")
	}

	// A span shorter than two fades gets half-length fades.
	short := FilterGraph([]Span{{"x", 100, 140}}, ModeBeep, media.AudioFormat{SampleRate: 44100, Channels: 1, Layout: "mono"})
	if !strings.Contains(short, "afade=t=in:st=0:d=0.020,afade=t=out:st=0.020:d=0.020,pan=mono|c0=c0,adelay=delays=100:all=1") {
		t.Errorf("short span fades: %s", short)
	}
}

func TestWriteAndSignature(t *testing.T) {
	p := filepath.Join(t.TempDir(), "censored.json")
	if err := Write(p, &Censored{Mode: ModeBeep}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if !strings.Contains(s, `"schema_version": "1"`) || !strings.Contains(s, `"spans": []`) || !strings.Contains(s, `"mode": "beep"`) {
		t.Errorf("empty censored.json: %s", s)
	}
	c := &Censored{Mode: ModeMute, Spans: []Span{{"a", 1, 2}, {"b", 3, 4}}}
	if got := Signature(c); got != "mute;1-2;3-4" {
		t.Errorf("signature: %s", got)
	}
}

func TestFindDefaultList(t *testing.T) {
	t.Setenv("LONGFORM_CENSOR_LIST", "")
	t.Setenv("LONGFORM_CONFIG_DIR", t.TempDir())
	p, err := FindDefaultList() // walks up from the package dir to the repo's plugins/ copy
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, filepath.Join("plugins", "longform-pipeline", "censor", "default.json")) {
		t.Errorf("found %s", p)
	}
	own := filepath.Join(t.TempDir(), "mine.json")
	os.WriteFile(own, []byte(`{"schema_version":"1","words":["x"]}`), 0o644)
	t.Setenv("LONGFORM_CENSOR_LIST", own)
	if p, err := FindDefaultList(); err != nil || p != own {
		t.Errorf("env override: %s %v", p, err)
	}
}
