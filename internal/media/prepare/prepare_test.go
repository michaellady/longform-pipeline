package prepare

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/censor"
	"github.com/michaellady/longform-pipeline/internal/media"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/transcribe"
)

// Integration: set LONGFORM_INTEGRATION=1 to run against the real ffmpeg,
// ffprobe and whisper-cli. The whisper model comes from LONGFORM_WHISPER_MODEL
// or, when unset, the local dev cache listed below.
const devModelCache = "/Users/mikelady/dev/claude-social-media-skills/.video-edit-cache/models/ggml-small.en.bin"

func integrationTools(t *testing.T) media.Tools {
	t.Helper()
	if os.Getenv("LONGFORM_INTEGRATION") != "1" {
		t.Skip("set LONGFORM_INTEGRATION=1 to run against ffmpeg/whisper-cli")
	}
	tools, err := media.FindTools()
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func makeFixture(t *testing.T, tools media.Tools, dir string, hdr bool) string {
	t.Helper()
	out := filepath.Join(dir, "fixture.mov")
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30",
		"-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-t", "3"}
	if hdr {
		args = append(args, "-vf", "format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=arib-std-b67:colorspace=bt2020nc")
	} else {
		args = append(args, "-pix_fmt", "yuv420p")
	}
	args = append(args, "-c:v", "libx264", "-c:a", "aac", "-shortest", out)
	if b, err := exec.Command(tools.FFmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, b)
	}
	return out
}

func stagedJob(t *testing.T, fixture string) *model.Job {
	t.Helper()
	t.Setenv("LONGFORM_DATA_DIR", t.TempDir())
	t.Setenv("LONGFORM_CONFIG_DIR", t.TempDir())
	job, err := model.NewJob("test", "deadbeefcafe", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(job.Dir(), "sources", "fixture.mov")
	b, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
	job.Sources = []model.Source{{ID: "s1", Path: "sources/fixture.mov", Original: fixture, SizeBytes: int64(len(b))}}
	if err := job.Advance(model.StateStaged, "test", ""); err != nil {
		t.Fatal(err)
	}
	return job
}

func whisperEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("LONGFORM_WHISPER_MODEL") == "" {
		if _, err := os.Stat(devModelCache); err != nil {
			t.Skipf("no whisper model: set LONGFORM_WHISPER_MODEL (dev cache %s absent)", devModelCache)
		}
		t.Setenv("LONGFORM_WHISPER_MODEL", devModelCache)
	}
	if _, err := transcribe.FindCLI(); err != nil {
		t.Skip(err)
	}
}

func TestIntegrationPrepareSDRStabilize(t *testing.T) {
	tools := integrationTools(t)
	whisperEnv(t)
	fixture := makeFixture(t, tools, t.TempDir(), false)
	job := stagedJob(t, fixture)
	prof := &model.Profile{Name: "test", Inbox: "/tmp", Orientation: "landscape",
		Stabilize:  model.StabilizeConfig{Enabled: true, Shakiness: 5, Smoothing: 10, Zoom: 0},
		Censor:     model.CensorConfig{Enabled: true, Mode: "beep"},
		Transcribe: model.TranscribeConfig{Language: "en"}}
	var progress strings.Builder
	opts := Options{Tools: tools, Log: &progress}

	if err := Run(job, prof, opts); err != nil {
		t.Fatalf("prepare: %v\nprogress:\n%s", err, progress.String())
	}
	t.Logf("progress:\n%s", progress.String())
	if job.State != model.StatePrepared {
		t.Fatalf("state = %s", job.State)
	}
	for _, rel := range []string{DeliveryOut, TranscriptOut, CensoredOut, StabilizeBefore, StabilizeAfter, RemuxOut, StabilizedOut, TransformsOut, WhisperOut, LogOut} {
		if st, err := os.Stat(job.Path(rel)); err != nil || st.Size() == 0 {
			t.Errorf("missing or empty %s: %v", rel, err)
		}
	}
	if job.Artifacts.Delivery != DeliveryOut || job.Artifacts.Transcript != TranscriptOut || job.Artifacts.Censored != CensoredOut || job.Artifacts.StabilizeBefore != StabilizeBefore || job.Artifacts.StabilizeAfter != StabilizeAfter {
		t.Errorf("artifacts: %+v", job.Artifacts)
	}
	if job.Delivery == nil || job.Delivery.Codec != "h264" || job.Delivery.AudioCodec != "aac" || job.Delivery.DurationMs < 2900 {
		t.Errorf("delivery probe: %+v", job.Delivery)
	}
	tr, err := transcribe.Load(job.Path(TranscriptOut))
	if err != nil || tr.Source != "whisper.cpp" || tr.Words == nil {
		t.Errorf("transcript: %+v %v", tr, err)
	}
	cb, _ := os.ReadFile(job.Path(CensoredOut))
	if !strings.Contains(string(cb), `"spans": []`) || !strings.Contains(string(cb), `"mode": "beep"`) {
		t.Errorf("censored.json on silence should have empty spans: %s", cb)
	}

	// Re-load from disk and run again: every step must be skipped.
	again, err := model.LoadJob(job.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if again.State != model.StatePrepared {
		t.Fatalf("persisted state = %s", again.State)
	}
	if err := Run(again, prof, opts); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	ex, _ := again.Extra["prepare"].(map[string]any)
	if ex == nil || ex["stabilized"] != true || ex["hdr"] != false {
		t.Errorf("extra.prepare = %+v", again.Extra["prepare"])
	}
}

func TestIntegrationPrepareHDRTonemap(t *testing.T) {
	tools := integrationTools(t)
	t.Setenv("LONGFORM_SKIP_TRANSCRIBE", "1")
	fixture := makeFixture(t, tools, t.TempDir(), true)
	job := stagedJob(t, fixture)
	prof := &model.Profile{Name: "test", Inbox: "/tmp", Orientation: "landscape", Censor: model.CensorConfig{Enabled: true, Mode: "mute"}}
	var progress strings.Builder
	if err := Run(job, prof, Options{Tools: tools, Log: &progress}); err != nil {
		t.Fatalf("prepare: %v\nprogress:\n%s", err, progress.String())
	}
	t.Logf("progress:\n%s", progress.String())
	if job.State != model.StatePrepared {
		t.Fatalf("state = %s", job.State)
	}
	if !job.Sources[0].Probe.HDR || job.Sources[0].Probe.ColorTransfer != "arib-std-b67" {
		t.Errorf("source probe: %+v", job.Sources[0].Probe)
	}
	if job.Delivery.ColorTransfer != "bt709" || job.Delivery.HDR {
		t.Errorf("delivery not tonemapped: %+v", job.Delivery)
	}
	ex, _ := job.Extra["prepare"].(map[string]any)
	if ex["tonemap_filter"] != "zscale" || ex["hdr"] != true || ex["transcribe_skipped"] != true {
		t.Errorf("extra.prepare = %+v", ex)
	}
	tr, err := transcribe.Load(job.Path(TranscriptOut))
	if err != nil || tr.Source != "skipped" || len(tr.Words) != 0 {
		t.Errorf("skipped transcript: %+v %v", tr, err)
	}
	if _, err := os.Stat(job.Path(StabilizeBefore)); err == nil {
		t.Error("stabilize frames written although stabilize is disabled")
	}
}

// The beep render end to end: a fixed span on silent audio must come out at
// -12 dBFS (ffmpeg's sine source is 1/8 scale, so the x2 gain is load-bearing).
func TestIntegrationBeepLevel(t *testing.T) {
	tools := integrationTools(t)
	dir := t.TempDir()
	fixture := makeFixture(t, tools, dir, false)
	info, err := tools.Probe(fixture)
	if err != nil {
		t.Fatal(err)
	}
	spans := []censor.Span{{Word: "x", StartMs: 500, EndMs: 1000}}
	graph := censor.FilterGraph(spans, censor.ModeBeep, info.Audio())
	out := filepath.Join(dir, "beeped.mp4")
	if err := tools.RenderAudioGraph(dir, fixture, out, graph); err != nil {
		t.Fatal(err)
	}
	peak, err := tools.MaxVolumeDB(dir, out)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(peak-(-12)) > 0.5 {
		t.Errorf("beep peak = %.1f dB, want -12 dB", peak)
	}
	t.Logf("beep peak %.1f dBFS", peak)
	muted := filepath.Join(dir, "muted.mp4")
	if err := tools.RenderAudioGraph(dir, fixture, muted, censor.FilterGraph(spans, censor.ModeMute, info.Audio())); err != nil {
		t.Fatal(err)
	}
}

func TestRunRefusesWrongState(t *testing.T) {
	t.Setenv("LONGFORM_DATA_DIR", t.TempDir())
	job, err := model.NewJob("test", "0123456789ab", []model.Source{{ID: "s", Path: "sources/x.mov"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	err = Run(job, &model.Profile{}, Options{Tools: media.Tools{FFmpeg: "/nonexistent/ffmpeg", FFprobe: "/nonexistent/ffprobe"}})
	if err == nil || !strings.Contains(err.Error(), "not staged") {
		t.Errorf("detected job must be refused, got %v", err)
	}
	job.State = model.StatePrepared
	if err := Run(job, &model.Profile{}, Options{}); err != nil {
		t.Errorf("prepared job must be a no-op, got %v", err)
	}
}
