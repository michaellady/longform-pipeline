package youtube

import (
	"strings"
	"testing"
)

func TestFormatTimestamp(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "0:00"},
		{999, "0:00"},
		{1000, "0:01"},
		{65_000, "1:05"},
		{600_000, "10:00"},
		{3_599_000, "59:59"},
		{3_600_000, "1:00:00"},
		{3_661_000, "1:01:01"},
		{36_000_000, "10:00:00"},
		{-5, "0:00"},
	}
	for _, c := range cases {
		if got := FormatTimestamp(c.ms); got != c.want {
			t.Errorf("FormatTimestamp(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}

func threeChapters() []Chapter {
	return []Chapter{
		{AtMS: 0, Title: "Intro"},
		{AtMS: 65_000, Title: "Middle"},
		{AtMS: 3_600_000, Title: "End"},
	}
}

func TestChapterLines(t *testing.T) {
	got := ChapterLines(threeChapters())
	want := "0:00 Intro\n1:05 Middle\n1:00:00 End"
	if got != want {
		t.Fatalf("ChapterLines = %q, want %q", got, want)
	}
}

func TestValidateChapters(t *testing.T) {
	cases := []struct {
		name    string
		ch      []Chapter
		wantErr string
	}{
		{"ok", threeChapters(), ""},
		{"too few", threeChapters()[:2], "at least 3"},
		{"first not zero", []Chapter{{AtMS: 1000, Title: "a"}, {AtMS: 20_000, Title: "b"}, {AtMS: 40_000, Title: "c"}}, "start at 0"},
		{"equal timestamps", []Chapter{{0, "a"}, {20_000, "b"}, {20_000, "c"}}, "does not come after"},
		{"decreasing", []Chapter{{0, "a"}, {30_000, "b"}, {20_000, "c"}}, "does not come after"},
		{"gap under 10s", []Chapter{{0, "a"}, {9_999, "b"}, {30_000, "c"}}, "at least 10000 ms"},
		{"empty title", []Chapter{{0, "a"}, {20_000, "  "}, {40_000, "c"}}, "empty title"},
		{"newline title", []Chapter{{0, "a"}, {20_000, "b\nc"}, {40_000, "c"}}, "newline"},
	}
	for _, c := range cases {
		err := ValidateChapters(c.ch)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: got %v, want error containing %q", c.name, err, c.wantErr)
		}
	}
}

func TestParseChapters(t *testing.T) {
	ch, err := ParseChapters([]byte(`[{"at_ms":0,"title":"Intro"},{"at_ms":15000,"title":"Two"},{"at_ms":90000,"title":"Three"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 3 || ch[1].AtMS != 15000 || ch[2].Title != "Three" {
		t.Fatalf("unexpected chapters %+v", ch)
	}
	if _, err := ParseChapters([]byte(`{"at_ms":0}`)); err == nil {
		t.Fatal("object instead of array should fail")
	}
	if _, err := ParseChapters([]byte(`[{"at_ms":5,"title":"x"},{"at_ms":20000,"title":"y"},{"at_ms":40000,"title":"z"}]`)); err == nil {
		t.Fatal("first chapter not at 0 should fail")
	}
}

func TestParseChaptersFile(t *testing.T) {
	p := t.TempDir() + "/chapters.json"
	if err := writeFile(p, `[{"at_ms":0,"title":"A"},{"at_ms":10000,"title":"B"},{"at_ms":20000,"title":"C"}]`); err != nil {
		t.Fatal(err)
	}
	ch, err := ParseChaptersFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 3 {
		t.Fatalf("got %d chapters", len(ch))
	}
	if _, err := ParseChaptersFile(p + ".missing"); err == nil {
		t.Fatal("missing file should fail")
	}
}

func TestAppendChapters(t *testing.T) {
	ch := threeChapters()
	lines := "0:00 Intro\n1:05 Middle\n1:00:00 End"
	cases := []struct {
		name string
		desc string
		ch   []Chapter
		want string
	}{
		{"no chapters", "hello", nil, "hello"},
		{"empty description", "", ch, lines},
		{"plain description", "hello", ch, "hello\n\n" + lines},
		{"trailing newlines trimmed", "hello\n\n\n", ch, "hello\n\n" + lines},
		{"whitespace-only description", "  \n", ch, lines},
	}
	for _, c := range cases {
		if got := AppendChapters(c.desc, c.ch); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
