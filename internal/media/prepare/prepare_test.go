package prepare

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// encoderCase is one prepare.encoder setting and what it must resolve to on
// this machine.
type encoderCase struct {
	setting          string
	encoder, hwaccel string
}

// encoderCases runs each integration test with the hardware path ("auto",
// which is VideoToolbox on a Mac whose ffmpeg can use it) and the software
// path ("x264"). The expectation for "auto" comes from probing the real
// ffmpeg, so the tests also pass where VideoToolbox is absent.
func encoderCases(t *testing.T, tools media.Tools) []encoderCase {
	t.Helper()
	auto := encoderCase{setting: model.PrepareEncoderAuto, encoder: media.EncoderX264, hwaccel: media.HWAccelNone}
	if runtime.GOOS == "darwin" {
		caps, err := tools.VideoToolboxCaps()
		if err != nil {
			t.Fatal(err)
		}
		if caps.Encoder {
			auto.encoder = media.EncoderVideoToolbox
			if caps.HWAccel {
				auto.hwaccel = media.HWAccelVideoToolbox
			}
		}
	}
	t.Logf("auto resolves to %s, decode %s", auto.encoder, auto.hwaccel)
	return []encoderCase{auto, {setting: model.PrepareEncoderX264, encoder: media.EncoderX264, hwaccel: media.HWAccelNone}}
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
	for _, ec := range encoderCases(t, tools) {
		t.Run(ec.setting, func(t *testing.T) { testPrepareSDRStabilize(t, tools, ec) })
	}
}

func testPrepareSDRStabilize(t *testing.T, tools media.Tools, ec encoderCase) {
	fixture := makeFixture(t, tools, t.TempDir(), false)
	job := stagedJob(t, fixture)
	prof := &model.Profile{Name: "test", Inbox: "/tmp", Orientation: "landscape",
		Prepare:    model.PrepareConfig{Encoder: ec.setting},
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
	for _, rel := range []string{DeliveryOut, TranscriptOut, CensoredOut, StabilizeBefore, StabilizeAfter, RemuxOut, StabilizedOut, WhisperOut, LogOut} {
		if st, err := os.Stat(job.Path(rel)); err != nil || st.Size() == 0 {
			t.Errorf("missing or empty %s: %v", rel, err)
		}
	}
	// The method follows the local ffmpeg: vidstab writes a transforms file,
	// the deshake fallback does not.
	hasVidstab, err := tools.HasFilter("vidstabdetect")
	if err != nil {
		t.Fatal(err)
	}
	wantMethod := "deshake (vidstab filter not in this ffmpeg)"
	if hasVidstab {
		wantMethod = "vidstab"
		if st, err := os.Stat(job.Path(TransformsOut)); err != nil || st.Size() == 0 {
			t.Errorf("vidstab run without %s: %v", TransformsOut, err)
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
	// Read back from job.json on disk: the method must survive saveState.
	if ex["stabilize_method"] != wantMethod {
		t.Errorf("persisted stabilize_method = %v, want %q", ex["stabilize_method"], wantMethod)
	}
	// The stabilise pass re-encoded with the configured encoder.
	if ex["encoder"] != ec.encoder || ex["hwaccel"] != ec.hwaccel {
		t.Errorf("encoder/hwaccel = %v/%v, want %s/%s", ex["encoder"], ex["hwaccel"], ec.encoder, ec.hwaccel)
	}
}

func TestIntegrationPrepareHDRTonemap(t *testing.T) {
	tools := integrationTools(t)
	t.Setenv("LONGFORM_SKIP_TRANSCRIBE", "1")
	for _, ec := range encoderCases(t, tools) {
		t.Run(ec.setting, func(t *testing.T) { testPrepareHDRTonemap(t, tools, ec) })
	}
}

func testPrepareHDRTonemap(t *testing.T, tools media.Tools, ec encoderCase) {
	fixture := makeFixture(t, tools, t.TempDir(), true)
	job := stagedJob(t, fixture)
	prof := &model.Profile{Name: "test", Inbox: "/tmp", Orientation: "landscape",
		Prepare: model.PrepareConfig{Encoder: ec.setting},
		Censor:  model.CensorConfig{Enabled: true, Mode: "mute"}}
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
	if job.Delivery.ColorTransfer != "bt709" || job.Delivery.HDR || job.Delivery.Codec != "h264" {
		t.Errorf("delivery not tonemapped to H.264 BT.709: %+v", job.Delivery)
	}
	ex, _ := job.Extra["prepare"].(map[string]any)
	if (ex["tonemap_filter"] != "zscale" && ex["tonemap_filter"] != "tonemap-only") || ex["hdr"] != true || ex["transcribe_skipped"] != true {
		t.Errorf("extra.prepare = %+v", ex)
	}
	if ex["encoder"] != ec.encoder || ex["hwaccel"] != ec.hwaccel {
		t.Errorf("encoder/hwaccel = %v/%v, want %s/%s", ex["encoder"], ex["hwaccel"], ec.encoder, ec.hwaccel)
	}
	// The intermediate carries the BT.709 tags itself, not only the delivery
	// probe's view of them.
	rm, err := tools.Probe(job.Path(RemuxOut))
	if err != nil || rm.ColorTransfer != "bt709" || rm.ColorPrimaries != "bt709" || rm.ColorSpace != "bt709" || rm.VideoCodec != "h264" {
		t.Errorf("tonemapped intermediate: %+v %v", rm, err)
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

// State fields, stabilize_method included, must round-trip through
// job.extra.prepare in job.json; saveState replaces that map wholesale, so a
// key written beside it would be lost.
func TestStatePersistsAcrossSave(t *testing.T) {
	t.Setenv("LONGFORM_DATA_DIR", t.TempDir())
	job, err := model.NewJob("test", "0123456789ab", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{job: job, state: &State{Steps: map[string]Stamp{"remux": {Sig: "copy"}}}}
	r.state.Stabilized = true
	r.state.StabilizeMethod = "deshake (vidstab filter not in this ffmpeg)"
	r.state.Encoder, r.state.HWAccel = media.EncoderVideoToolbox, media.HWAccelVideoToolbox
	if err := r.saveState(); err != nil {
		t.Fatal(err)
	}
	// A later step saves again; the fields must still be there on disk.
	r.state.CensorSpans = 2
	if err := r.saveState(); err != nil {
		t.Fatal(err)
	}
	loaded, err := model.LoadJob(job.Dir())
	if err != nil {
		t.Fatal(err)
	}
	ex, _ := loaded.Extra["prepare"].(map[string]any)
	if ex["stabilize_method"] != r.state.StabilizeMethod || ex["encoder"] != media.EncoderVideoToolbox || ex["hwaccel"] != media.HWAccelVideoToolbox || ex["stabilized"] != true {
		t.Errorf("job.json extra.prepare = %+v", ex)
	}
	st := loadState(loaded)
	if st.StabilizeMethod != r.state.StabilizeMethod || st.CensorSpans != 2 || st.Steps["remux"].Sig != "copy" {
		t.Errorf("loadState = %+v", st)
	}
}

func TestEncoderSig(t *testing.T) {
	// libx264 keeps the signature stamps had before the encoder was
	// selectable, so an upgrade does not redo finished tonemaps.
	if got := encoderSig(media.VideoEncode{}); got != "" {
		t.Errorf("libx264 sig = %q, want empty", got)
	}
	if got := encoderSig(media.VideoEncode{Encoder: media.EncoderX264, HWAccel: media.HWAccelNone}); got != "" {
		t.Errorf("libx264 sig = %q, want empty", got)
	}
	vt := encoderSig(media.VideoEncode{Encoder: media.EncoderVideoToolbox, HWAccel: media.HWAccelVideoToolbox})
	if vt != ":h264_videotoolbox" {
		t.Errorf("videotoolbox sig = %q", vt)
	}
	if sw := encoderSig(media.VideoEncode{Encoder: media.EncoderVideoToolbox, HWAccel: media.HWAccelNone}); sw != vt {
		t.Errorf("hardware decode must not change the sig: %q vs %q", sw, vt)
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
