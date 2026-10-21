package transcribe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "whisper-full.json"))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := Normalize(raw, "/some/dir/ggml-small.en.bin", "xx", 2000)
	if err != nil {
		t.Fatal(err)
	}
	if tr.SchemaVersion != "1" || tr.Source != "whisper.cpp" || tr.Model != "ggml-small.en.bin" || tr.DurationMs != 2000 {
		t.Errorf("header: %+v", tr)
	}
	if tr.Language != "en" {
		t.Errorf("language = %q, want whisper's detected en", tr.Language)
	}
	want := []Word{
		{"Hello", 140, 380}, {"world,", 380, 940}, {"this", 940, 1240}, {"is", 1240, 1350}, {"a", 1350, 1430}, {"test.", 1430, 2000},
	}
	if len(tr.Words) != len(want) {
		t.Fatalf("words = %+v, want %+v", tr.Words, want)
	}
	for i := range want {
		if tr.Words[i] != want[i] {
			t.Errorf("word %d = %+v, want %+v", i, tr.Words[i], want[i])
		}
	}
	if len(tr.Segments) != 1 {
		t.Fatalf("segments = %+v, want one sentence", tr.Segments)
	}
	if s := tr.Segments[0]; s.StartMs != 140 || s.EndMs != 2000 || s.Text != "Hello world, this is a test." {
		t.Errorf("segment = %+v", s)
	}
}

func TestNormalizeMultiWordAndMarkers(t *testing.T) {
	raw := `{"result":{"language":""},"transcription":[
	  {"offsets":{"from":0,"to":100},"text":"[BLANK_AUDIO]"},
	  {"offsets":{"from":100,"to":400},"text":" one two"},
	  {"offsets":{"from":2000,"to":2500},"text":" (music) three."}
	]}`
	tr, err := Normalize([]byte(raw), "m.bin", "en", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Language != "en" {
		t.Errorf("language fallback = %q", tr.Language)
	}
	if len(tr.Words) != 3 {
		t.Fatalf("words = %+v", tr.Words)
	}
	if tr.Words[0] != (Word{"one", 100, 250}) || tr.Words[1] != (Word{"two", 250, 400}) {
		t.Errorf("split by character share: %+v", tr.Words[:2])
	}
	if tr.Words[2] != (Word{"three.", 2000, 2500}) {
		t.Errorf("marker not dropped: %+v", tr.Words[2])
	}
	if len(tr.Segments) != 2 {
		t.Errorf("gap > 1 s should split segments: %+v", tr.Segments)
	}
}

func TestBuildSegmentsEmpty(t *testing.T) {
	if segs := BuildSegments(nil); segs == nil || len(segs) != 0 {
		t.Errorf("want empty non-nil, got %#v", segs)
	}
	words := []Word{{"Hi.", 0, 100}, {"Bye?", 200, 300}, {"ok", 400, 500}}
	segs := BuildSegments(words)
	if len(segs) != 3 || segs[2].Text != "ok" {
		t.Errorf("segments = %+v", segs)
	}
}

func TestCleanWord(t *testing.T) {
	cases := map[string]string{"Fucking,": "fucking", "F*ck": "fck", "mother-fucker!": "motherfucker", "...": "", "Héllo": "héllo", "it's": "its"}
	for in, want := range cases {
		if got := CleanWord(in); got != want {
			t.Errorf("CleanWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestArgs(t *testing.T) {
	got := strings.Join(Args(Options{Model: "m.bin", Language: "en", Threads: 8}, "analysis/audio16k.wav", "analysis/whisper"), " ")
	want := "-m m.bin -f analysis/audio16k.wav -l en -t 8 -ojf -ml 1 -sow -np -of analysis/whisper"
	if got != want {
		t.Errorf("args:\n got %s\nwant %s", got, want)
	}
	got = strings.Join(Args(Options{Model: "m.bin"}, "a.wav", "out"), " ")
	if strings.Contains(got, "-t ") || strings.Contains(got, "-l ") {
		t.Errorf("zero threads/language must be omitted: %s", got)
	}
}

func TestModelPath(t *testing.T) {
	t.Setenv("LONGFORM_WHISPER_MODEL", "")
	t.Setenv("LONGFORM_CONFIG_DIR", "/cfg")
	if got := ModelPath("/p/model.bin"); got != "/p/model.bin" {
		t.Errorf("profile wins: %s", got)
	}
	if got := ModelPath(""); got != "/cfg/models/"+DefaultModelName {
		t.Errorf("default: %s", got)
	}
	t.Setenv("LONGFORM_WHISPER_MODEL", "/env/model.bin")
	if got := ModelPath(""); got != "/env/model.bin" {
		t.Errorf("env: %s", got)
	}
}

func TestWriteLoadEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "transcript.json")
	if err := Write(p, Empty("en", 1234)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"words": []`) || !strings.Contains(string(b), `"segments": []`) {
		t.Errorf("empty arrays must serialise as [], got %s", b)
	}
	tr, err := Load(p)
	if err != nil || tr.Source != "skipped" || tr.DurationMs != 1234 {
		t.Errorf("load: %+v %v", tr, err)
	}
}
