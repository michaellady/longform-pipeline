package youtube

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func baseRequest() Request {
	return Request{
		File:        "/tmp/video.mp4",
		Title:       "  A walk and a talk  ",
		Description: "First line.\n",
		Privacy:     PrivacyPublic,
		Tags:        []string{"go", " vibe code "},
		CategoryID:  "28",
	}
}

func TestBuildVideoBasic(t *testing.T) {
	r := baseRequest()
	v, err := BuildVideo(&r)
	if err != nil {
		t.Fatal(err)
	}
	if v.Snippet.Title != "A walk and a talk" {
		t.Errorf("title %q not trimmed", v.Snippet.Title)
	}
	if v.Snippet.Description != "First line.\n" {
		t.Errorf("description changed without chapters: %q", v.Snippet.Description)
	}
	if !slices.Equal(v.Snippet.Tags, []string{"go", "vibe code"}) {
		t.Errorf("tags %v", v.Snippet.Tags)
	}
	if v.Snippet.CategoryId != "28" {
		t.Errorf("category %q", v.Snippet.CategoryId)
	}
	if v.Status.PrivacyStatus != PrivacyPublic {
		t.Errorf("privacy %q", v.Status.PrivacyStatus)
	}
	if v.Status.PublishAt != "" {
		t.Errorf("publishAt should be empty, got %q", v.Status.PublishAt)
	}
	if v.Status.SelfDeclaredMadeForKids {
		t.Error("made for kids should default to false")
	}
	if !slices.Contains(v.Status.ForceSendFields, "SelfDeclaredMadeForKids") {
		t.Error("SelfDeclaredMadeForKids must be force-sent so false reaches YouTube")
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"selfDeclaredMadeForKids":false`) {
		t.Errorf("encoded body omits the false made-for-kids declaration: %s", b)
	}
	if strings.Contains(string(b), `"publishAt"`) {
		t.Errorf("encoded body should not carry publishAt: %s", b)
	}
}

func TestBuildVideoPublishAtForcesPrivate(t *testing.T) {
	r := baseRequest()
	r.Privacy = ""
	r.PublishAt = time.Now().Add(2 * time.Hour).In(time.FixedZone("PDT", -7*3600))
	v, err := BuildVideo(&r)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status.PrivacyStatus != PrivacyPrivate {
		t.Errorf("privacy %q, want private when scheduling", v.Status.PrivacyStatus)
	}
	if !strings.HasSuffix(v.Status.PublishAt, "Z") {
		t.Errorf("publishAt %q should be UTC RFC3339", v.Status.PublishAt)
	}
	parsed, err := time.Parse(time.RFC3339, v.Status.PublishAt)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(r.PublishAt.Truncate(time.Second)) {
		t.Errorf("publishAt %s != %s", parsed, r.PublishAt)
	}
}

func TestBuildVideoAppendsChapters(t *testing.T) {
	r := baseRequest()
	r.Chapters = threeChapters()
	v, err := BuildVideo(&r)
	if err != nil {
		t.Fatal(err)
	}
	want := "First line.\n\n0:00 Intro\n1:05 Middle\n1:00:00 End"
	if v.Snippet.Description != want {
		t.Errorf("description:\n%q\nwant:\n%q", v.Snippet.Description, want)
	}
}

func TestEffectivePrivacy(t *testing.T) {
	r := Request{}
	if r.EffectivePrivacy() != PrivacyPrivate {
		t.Error("unset privacy should be private")
	}
	r.Privacy = PrivacyUnlisted
	if r.EffectivePrivacy() != PrivacyUnlisted {
		t.Error("explicit unlisted should stay")
	}
	r.Privacy = PrivacyPrivate
	r.PublishAt = time.Now().Add(time.Hour)
	if r.EffectivePrivacy() != PrivacyPrivate {
		t.Error("scheduled should be private")
	}
}

func TestValidate(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		mutate  func(r *Request)
		wantErr string
	}{
		{"ok", func(r *Request) {}, ""},
		{"ok unlisted no tags", func(r *Request) { r.Privacy = PrivacyUnlisted; r.Tags = nil; r.CategoryID = "" }, ""},
		{"ok scheduled default privacy", func(r *Request) { r.Privacy = ""; r.PublishAt = now.Add(time.Hour) }, ""},
		{"ok scheduled explicit private", func(r *Request) { r.Privacy = PrivacyPrivate; r.PublishAt = now.Add(time.Hour) }, ""},
		{"missing file", func(r *Request) { r.File = "" }, "--file is required"},
		{"missing title", func(r *Request) { r.Title = "   " }, "--title is required"},
		{"title too long", func(r *Request) { r.Title = strings.Repeat("x", 101) }, "100"},
		{"title angle brackets", func(r *Request) { r.Title = "a <b>" }, "< or >"},
		{"description too long", func(r *Request) { r.Description = strings.Repeat("x", 5001) }, "5000"},
		{"description plus chapters too long", func(r *Request) {
			r.Description = strings.Repeat("x", 4990)
			r.Chapters = threeChapters()
		}, "5000"},
		{"bad privacy", func(r *Request) { r.Privacy = "hidden" }, "--privacy must be"},
		{"publish-at with public", func(r *Request) { r.PublishAt = now.Add(time.Hour) }, "requires --privacy private"},
		{"publish-at in past", func(r *Request) { r.Privacy = ""; r.PublishAt = now.Add(-time.Minute) }, "not in the future"},
		{"publish-at exactly now", func(r *Request) { r.Privacy = ""; r.PublishAt = now }, "not in the future"},
		{"bad chapters", func(r *Request) { r.Chapters = threeChapters()[:1] }, "at least 3"},
		{"empty tag", func(r *Request) { r.Tags = []string{"a", " "} }, "tags must not be empty"},
		{"tags too long", func(r *Request) { r.Tags = []string{strings.Repeat("a", 300), strings.Repeat("b", 201)} }, "500"},
		{"tag angle brackets", func(r *Request) { r.Tags = []string{"<x>"} }, "< or >"},
		{"non-numeric category", func(r *Request) { r.CategoryID = "tech" }, "numeric"},
		{"bad thumbnail extension", func(r *Request) { r.Thumbnail = "/tmp/thumb.gif" }, ".jpg"},
		{"ok thumbnail png", func(r *Request) { r.Thumbnail = "/tmp/thumb.PNG" }, ""},
	}
	for _, c := range cases {
		r := baseRequest()
		c.mutate(&r)
		err := r.validateAt(now)
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
