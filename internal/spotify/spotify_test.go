package spotify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/media"
)

const ytDescription = `Why enterprise AI pilots stall, and the one change that unsticks them.

Get the newsletter: https://example.com/newsletter?utm_source=youtube&utm_medium=video&utm_campaign=ep7
Free guide → https://example.com/guide?utm_source=youtube.

00:00 Intro
02:15 The pilot trap
#AI #EnterpriseAI #VibeCoding



Q&A at the end.
[opus:clip-abc123]`

func TestNotes(t *testing.T) {
	got := Notes(ytDescription, "")
	want := `Why enterprise AI pilots stall, and the one change that unsticks them.

Get the newsletter: https://example.com/newsletter?utm_source=spotify&utm_medium=video&utm_campaign=ep7
Free guide → https://example.com/guide?utm_source=spotify.

00:00 Intro
02:15 The pilot trap

Q&A at the end.`
	if got != want {
		t.Errorf("Notes:\n%s\n--- want ---\n%s", got, want)
	}
	if strings.Contains(Notes(ytDescription, "pod"), "utm_source=spotify") || !strings.Contains(Notes(ytDescription, "pod"), "utm_source=pod&") {
		t.Errorf("custom utm source not applied: %s", Notes(ytDescription, "pod"))
	}
	// A tag at the end of a text line goes too; a mid-text # word stays.
	if got := Notes("Watch this #1 trick [opus:x9]\n#tag", "spotify"); got != "Watch this #1 trick" {
		t.Errorf("inline tag: %q", got)
	}
}

func TestNotesHTML(t *testing.T) {
	got := NotesHTML("Line one & more <b>\nLine two: https://ex.com/a?x=1&y=2.\n\nPara two")
	want := `<p>Line one &amp; more &lt;b&gt;<br>Line two: <a href="https://ex.com/a?x=1&amp;y=2">https://ex.com/a?x=1&amp;y=2</a>.</p>` + "\n<p>Para two</p>"
	if got != want {
		t.Errorf("NotesHTML:\n%s\n--- want ---\n%s", got, want)
	}
	if NotesHTML("  ") != "" {
		t.Error("empty notes must render empty")
	}
}

func TestCheckLimits(t *testing.T) {
	if p := CheckLimits("Title", strings.Repeat("é", MaxDescriptionRunes)); len(p) != 0 {
		t.Errorf("4000 runes (8000 bytes) must pass: %v", p)
	}
	p := CheckLimits(strings.Repeat("t", MaxTitleRunes+1), strings.Repeat("x", MaxDescriptionRunes+1))
	if len(p) != 2 || !strings.Contains(p[0], "title is 201") || !strings.Contains(p[1], "notes are 4001") {
		t.Errorf("over limits: %v", p)
	}
	if p := CheckLimits(" ", "x"); len(p) != 1 || p[0] != "title is empty" {
		t.Errorf("empty title: %v", p)
	}
}

func TestIs16x9AndDisplaySize(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want bool
	}{{1920, 1080, true}, {3840, 2160, true}, {1280, 720, true}, {1920, 1088, true}, {1080, 1920, false}, {1440, 1080, false}, {0, 0, false}} {
		if got := Is16x9(c.w, c.h); got != c.want {
			t.Errorf("Is16x9(%d,%d) = %v", c.w, c.h, got)
		}
	}
	w, h := DisplaySize(&media.Info{Width: 1920, Height: 1080, Rotation: 90})
	if w != 1080 || h != 1920 {
		t.Errorf("rotated display size %dx%d", w, h)
	}
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestEncodeArgs(t *testing.T) {
	portrait := &media.Info{Width: 1080, Height: 1920, VideoCodec: "h264", AudioCodec: "aac", HasAudio: true}
	args, re := EncodeArgs("in.mp4", "out.mp4", portrait, media.EncoderVideoToolbox)
	wantFilter := "[0:v]split[a][b];[a]scale=1920:1080:force_original_aspect_ratio=increase,crop=1920:1080,boxblur=40:2,eq=brightness=-0.12[bg];[b]scale=-2:1080[fg];[bg][fg]overlay=(W-w)/2:0,format=yuv420p[v]"
	if !re || argAfter(args, "-filter_complex") != wantFilter {
		t.Errorf("portrait filter:\n%s", argAfter(args, "-filter_complex"))
	}
	joined := strings.Join(args, " ")
	for _, s := range []string{"-i in.mp4", "-map [v] -map 0:a?", "-c:v h264_videotoolbox -profile:v high -b:v 8M -maxrate 12M", "-r 30", "-c:a copy", "-movflags +faststart out.mp4"} {
		if !strings.Contains(joined, s) {
			t.Errorf("portrait args missing %q: %s", s, joined)
		}
	}
	if args[len(args)-1] != "out.mp4" {
		t.Errorf("output must be last: %s", joined)
	}

	args, _ = EncodeArgs("in.mp4", "out.mp4", &media.Info{Width: 1080, Height: 1920, VideoCodec: "hevc", AudioCodec: "opus"}, media.EncoderX264)
	joined = strings.Join(args, " ")
	if !strings.Contains(joined, "-c:v libx264 -profile:v high -preset medium -crf 18") || !strings.Contains(joined, "-c:a aac -b:a 192k") {
		t.Errorf("libx264 / non-aac args: %s", joined)
	}

	args, re = EncodeArgs("in.mp4", "out.mp4", &media.Info{Width: 1920, Height: 1080, VideoCodec: "h264", AudioCodec: "aac"}, "")
	joined = strings.Join(args, " ")
	if re || !strings.Contains(joined, "-c:v copy") || strings.Contains(joined, "filter") {
		t.Errorf("16:9 h264 must stream-copy: %s", joined)
	}

	args, re = EncodeArgs("in.mp4", "out.mp4", &media.Info{Width: 3840, Height: 2160, VideoCodec: "hevc", AudioCodec: "aac"}, media.EncoderX264)
	if !re || argAfter(args, "-vf") != "scale=1920:1080,format=yuv420p" {
		t.Errorf("16:9 hevc must re-encode at 1080p: %v", args)
	}

	// Wider than 16:9 is letterboxed.
	if f := PillarboxFilter(2560, 1080); !strings.Contains(f, "[b]scale=1920:-2[fg];[bg][fg]overlay=0:(H-h)/2") {
		t.Errorf("ultrawide filter: %s", f)
	}
}

func TestURLExpired(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, c := range []struct {
		u              string
		expired, known bool
	}{
		{"https://storage.googleapis.com/b/o.mp4?GoogleAccessId=x&Expires=1799999999&Signature=s", true, true},
		{"https://storage.googleapis.com/b/o.mp4?Expires=1800000600&Signature=s", false, true},
		{"https://x/o.mp4?X-Goog-Date=20270101T000000Z&X-Goog-Expires=604800", true, true},
		{"https://x/o.mp4?X-Amz-Date=20270115T075500Z&X-Amz-Expires=3600", false, true},
		{"https://x/o.mp4", false, false},
	} {
		e, k := URLExpired(c.u, now)
		if e != c.expired || k != c.known {
			t.Errorf("URLExpired(%s) = %v,%v want %v,%v", c.u, e, k, c.expired, c.known)
		}
	}
}

// --- Prepare on a stub ffmpeg/ffprobe (this test binary re-executed). ---

const (
	stubEnv    = "LONGFORM_SPOTIFY_FF_STUB"
	stubDirEnv = "LONGFORM_SPOTIFY_FF_STUB_DIR"
)

func TestMain(m *testing.M) {
	if os.Getenv(stubEnv) == "1" {
		os.Exit(ffStub(os.Args[1:]))
	}
	os.Exit(m.Run())
}

const (
	probePortrait = `{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1080,"height":1920,"r_frame_rate":"30/1","avg_frame_rate":"30/1","duration":"600.0"},{"index":1,"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2,"duration":"600.0"}],"format":{"duration":"600.0"}}`
	probeEpisode  = `{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"r_frame_rate":"30/1","avg_frame_rate":"30/1","duration":"600.0"},{"index":1,"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2,"duration":"600.0"}],"format":{"duration":"600.0"}}`
)

func ffStub(args []string) int {
	dir := os.Getenv(stubDirEnv)
	if f, err := os.OpenFile(filepath.Join(dir, "calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	has := func(s string) bool {
		for _, a := range args {
			if a == s {
				return true
			}
		}
		return false
	}
	last := args[len(args)-1]
	switch {
	case has("-show_streams"):
		if strings.Contains(last, "episode-1080p") {
			fmt.Print(probeEpisode)
		} else {
			fmt.Print(probePortrait)
		}
	case has("-hwaccels"), has("-encoders"), has("-filters"), has("lavfi"):
		// no VideoToolbox: auto resolves to libx264
	default:
		if err := os.WriteFile(last, []byte("stub encode"), 0o644); err != nil {
			return 1
		}
	}
	return 0
}

func stubTools(t *testing.T) (media.Tools, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(stubEnv, "1")
	t.Setenv(stubDirEnv, dir)
	return media.Tools{FFmpeg: os.Args[0], FFprobe: os.Args[0]}, dir
}

func calls(t *testing.T, dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	return string(b)
}

func TestPrepareFallsBackToDeliveryAndIsIdempotent(t *testing.T) {
	tools, stubDir := stubTools(t)
	job := t.TempDir()
	if err := os.WriteFile(filepath.Join(job, DeliveryFile), []byte("movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(job, DeliveryFile), past, past)
	downloads := 0
	o := Options{
		JobDir: job, Title: "Episode title", Description: ytDescription, Tools: tools, GOOS: "linux",
		FreshExportURL: func(context.Context) (string, error) { return "", errors.New("OPUSCLIP_API_KEY not set") },
		ClipsExportURL: "https://storage.googleapis.com/b/o.mp4?Expires=1000&Signature=s",
		Download:       func(context.Context, string, string) error { downloads++; return nil },
	}
	res, err := Prepare(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.SourceKind != SourceDelivery || !strings.Contains(res.SourceNote, "OPUSCLIP_API_KEY") || !strings.Contains(res.SourceNote, "expired") || downloads != 0 {
		t.Errorf("source: %+v downloads=%d", res, downloads)
	}
	if !res.Reencoded || res.Encoder != media.EncoderX264 || res.Width != 1920 || res.Height != 1080 || res.DurationSec != 600 || len(res.Problems) != 0 {
		t.Errorf("result: %+v", res)
	}
	if c := calls(t, stubDir); !strings.Contains(c, "-filter_complex [0:v]split[a][b]") || !strings.Contains(c, "-c:v libx264") {
		t.Errorf("encode call: %s", c)
	}
	if b, _ := os.ReadFile(res.NotesText); !strings.Contains(string(b), "utm_source=spotify") || strings.Contains(string(b), "[opus:") {
		t.Errorf("notes text: %s", b)
	}
	if b, _ := os.ReadFile(res.NotesHTML); !strings.HasPrefix(string(b), "<p>") {
		t.Errorf("notes html: %s", b)
	}
	if _, err := os.Stat(res.Video + ".partial.mp4"); !os.IsNotExist(err) {
		t.Error("partial output left behind")
	}

	os.Remove(filepath.Join(stubDir, "calls.log"))
	res, err = Prepare(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.VideoSkipped || !res.NotesSkipped || strings.Contains(calls(t, stubDir), "filter_complex") {
		t.Errorf("second run must skip: %+v calls=%s", res, calls(t, stubDir))
	}
	o.Force = true
	res, err = Prepare(context.Background(), o)
	if err != nil || res.VideoSkipped || !strings.Contains(calls(t, stubDir), "filter_complex") {
		t.Errorf("--force must re-encode: %v %+v", err, res)
	}
}

func TestPrepareDownloadsFreshExportOnce(t *testing.T) {
	tools, _ := stubTools(t)
	job := t.TempDir()
	var got []string
	o := Options{
		JobDir: job, Title: "T", Description: "d", Tools: tools, GOOS: "linux",
		FreshExportURL: func(context.Context) (string, error) { return "https://fresh/export.mp4", nil },
		Download: func(_ context.Context, u, dst string) error {
			got = append(got, u)
			return os.WriteFile(dst, []byte("movie"), 0o644)
		},
	}
	res, err := Prepare(context.Background(), o)
	if err != nil || res.SourceKind != SourceFresh || res.Source != filepath.Join(job, SourceCacheFile) || len(got) != 1 {
		t.Fatalf("fresh: %v %+v %v", err, res, got)
	}
	res, err = Prepare(context.Background(), o)
	if err != nil || res.SourceKind != SourceCached || len(got) != 1 {
		t.Errorf("cached: %v %+v %v", err, res, got)
	}
}

func TestPrepareRefusesOverLimitNotes(t *testing.T) {
	tools, _ := stubTools(t)
	job := t.TempDir()
	src := filepath.Join(job, "src.mp4")
	os.WriteFile(src, []byte("movie"), 0o644)
	res, err := Prepare(context.Background(), Options{JobDir: job, Source: src, Title: "T", Description: strings.Repeat("x", 4001), Tools: tools, GOOS: "linux"})
	if !errors.Is(err, ErrLimits) || res.SourceKind != SourceFlag || len(res.Problems) != 1 {
		t.Errorf("over-limit: %v %+v", err, res)
	}
}
