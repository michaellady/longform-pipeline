package prepare

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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
	return makeRotatedFixture(t, tools, dir, hdr, 0)
}

// makeRotatedFixture is makeFixture with a display matrix: rotation is what
// ffprobe will report (-90 is an iPhone held upright), set by a stream-copy
// remux as a phone writes it, so the coded frames stay 320x240.
func makeRotatedFixture(t *testing.T, tools media.Tools, dir string, hdr bool, rotation int) string {
	t.Helper()
	out := filepath.Join(dir, "fixture.mov")
	if rotation != 0 {
		out = filepath.Join(dir, "unrotated.mov")
	}
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
	if rotation == 0 {
		return out
	}
	rotated := filepath.Join(dir, "fixture.mov")
	if b, err := exec.Command(tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-display_rotation", strconv.Itoa(rotation),
		"-i", out, "-c", "copy", rotated).CombinedOutput(); err != nil {
		t.Fatalf("rotated fixture: %v\n%s", err, b)
	}
	if info, err := tools.Probe(rotated); err != nil || info.Width != 320 || info.Height != 240 || info.Rotation != ((rotation%360)+360)%360 {
		t.Fatalf("rotated fixture probe: %+v %v", info, err)
	}
	return rotated
}

// frameSSIM compares the frame at atMs of two files (grey, scaled to
// 128x128), each decoded in software so a display matrix is applied: a
// correctly turned delivery scores near 0.9 against its autorotated source,
// a wrongly turned one under 0.3.
func frameSSIM(t *testing.T, tools media.Tools, a, b string, atMs int64) float64 {
	t.Helper()
	ss := media.FormatSeconds(atMs)
	out, err := exec.Command(tools.FFmpeg, "-hide_banner", "-ss", ss, "-i", a, "-ss", ss, "-i", b,
		"-filter_complex", "[0:v]scale=128:128,format=gray[a];[1:v]scale=128:128,format=gray[b];[a][b]ssim",
		"-frames:v", "1", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ssim: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`All:([0-9.]+)`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("ssim: no score in\n%s", out)
	}
	v, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// expectedTonemap is the conversion prepare must pick for an HDR source with
// this rotation on this machine, from the real ffmpeg.
func expectedTonemap(t *testing.T, tools media.Tools, enc media.VideoEncode, rotation int) (media.TonemapPlan, error) {
	t.Helper()
	return media.ChooseTonemap(runtime.GOOS, enc, rotation, tools.VideoToolboxCaps,
		func() (bool, error) { return tools.HasFilter("zscale") })
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
		for _, rot := range []int{0, -90} {
			t.Run(fmt.Sprintf("%s/rotation%d", ec.setting, rot), func(t *testing.T) { testPrepareHDRTonemap(t, tools, ec, rot) })
		}
	}
}

func testPrepareHDRTonemap(t *testing.T, tools media.Tools, ec encoderCase, rotation int) {
	fixture := makeRotatedFixture(t, tools, t.TempDir(), true, rotation)
	job := stagedJob(t, fixture)
	prof := &model.Profile{Name: "test", Inbox: "/tmp", Orientation: "landscape",
		Prepare: model.PrepareConfig{Encoder: ec.setting},
		Censor:  model.CensorConfig{Enabled: true, Mode: "mute"}}
	plan, planErr := expectedTonemap(t, tools, media.VideoEncode{Encoder: ec.encoder, HWAccel: ec.hwaccel}, rotation)
	t.Logf("expected tonemap: %s (%v)", plan.Filter, planErr)
	if runtime.GOOS == "darwin" {
		if caps, err := tools.VideoToolboxCaps(); err == nil && caps.HWAccel && caps.Encoder && caps.ScaleVT && caps.TransposeVT && plan.Filter != media.TonemapScaleVT {
			t.Fatalf("a Mac with VideoToolbox and scale_vt must tonemap with scale_vt, plan is %s", plan.Filter)
		}
	}
	var progress strings.Builder
	err := Run(job, prof, Options{Tools: tools, Log: &progress})
	t.Logf("progress:\n%s", progress.String())
	if plan.Filter == media.TonemapUnavailable {
		// No correct conversion here: prepare must fail loudly, not encode.
		if !errors.Is(err, media.ErrNoHDRConversion) || job.State != model.StateStaged {
			t.Fatalf("want ErrNoHDRConversion with the job left staged, got %v (%s)", err, job.State)
		}
		if _, serr := os.Stat(job.Path(RemuxOut)); serr == nil {
			t.Error("an HDR source without a correct conversion was encoded")
		}
		return
	}
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if job.State != model.StatePrepared {
		t.Fatalf("state = %s", job.State)
	}
	if !job.Sources[0].Probe.HDR || job.Sources[0].Probe.ColorTransfer != "arib-std-b67" {
		t.Errorf("source probe: %+v", job.Sources[0].Probe)
	}
	if job.Delivery.ColorTransfer != "bt709" || job.Delivery.HDR || job.Delivery.Codec != "h264" {
		t.Errorf("delivery not tonemapped to H.264 BT.709: %+v", job.Delivery)
	}
	// Upright with no rotation tag left: a -90 source comes out portrait.
	wantW, wantH := 320, 240
	if rotation == 90 || rotation == -90 {
		wantW, wantH = 240, 320
	}
	if job.Delivery.Width != wantW || job.Delivery.Height != wantH || job.Delivery.Rotation != 0 {
		t.Errorf("delivery %dx%d rotation %d, want %dx%d rotation 0", job.Delivery.Width, job.Delivery.Height, job.Delivery.Rotation, wantW, wantH)
	}
	ex, _ := job.Extra["prepare"].(map[string]any)
	if ex["tonemap_filter"] != plan.Filter || ex["hdr"] != true || ex["transcribe_skipped"] != true {
		t.Errorf("extra.prepare = %+v, want tonemap_filter %s", ex, plan.Filter)
	}
	if ex["encoder"] != ec.encoder || ex["hwaccel"] != ec.hwaccel {
		t.Errorf("encoder/hwaccel = %v/%v, want %s/%s", ex["encoder"], ex["hwaccel"], ec.encoder, ec.hwaccel)
	}
	if h := job.History[len(job.History)-1]; !strings.Contains(h.Note, "tonemap="+plan.Filter) {
		t.Errorf("history note %q lacks tonemap=%s", h.Note, plan.Filter)
	}
	// The intermediate carries the BT.709 tags itself, not only the delivery
	// probe's view of them.
	rm, err := tools.Probe(job.Path(RemuxOut))
	if err != nil || rm.ColorTransfer != "bt709" || rm.ColorPrimaries != "bt709" || rm.ColorSpace != "bt709" || rm.VideoCodec != "h264" {
		t.Errorf("tonemapped intermediate: %+v %v", rm, err)
	}
	// Brightness: the old tonemap-without-zscale chain made this frame about
	// 0.41x the naive (HDR read as SDR) render; a correct conversion of this
	// synthetic frame lands near 0.9x.
	const at = 1500
	got, err := tools.MeanLuma(job.Dir(), DeliveryOut, at)
	if err != nil {
		t.Fatal(err)
	}
	naive, err := tools.MeanLuma(job.Dir(), fixture, at)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("mean luma: delivery %.1f, naive render %.1f (ratio %.2f)", got, naive, got/naive)
	if got < 0.75*naive || got > 1.25*naive {
		t.Errorf("delivery mean luma %.1f is outside +-25%% of the naive render's %.1f", got, naive)
	}
	// Turned the same way as ffmpeg's autorotate turns the source.
	if s := frameSSIM(t, tools, job.Path(DeliveryOut), fixture, at); s < 0.7 {
		t.Errorf("delivery frame vs autorotated source SSIM %.3f, want >= 0.7 (wrong rotation?)", s)
	} else {
		t.Logf("orientation SSIM %.3f", s)
	}
	tr, err := transcribe.Load(job.Path(TranscriptOut))
	if err != nil || tr.Source != "skipped" || len(tr.Words) != 0 {
		t.Errorf("skipped transcript: %+v %v", tr, err)
	}
	if _, err := os.Stat(job.Path(StabilizeBefore)); err == nil {
		t.Error("stabilize frames written although stabilize is disabled")
	}
}

// A real iPhone HDR recording through prepare: set LONGFORM_HDR_SLICE to a
// short HLG clip (a 10 s slice of a 4K portrait recording was used). The
// frame at 5 s must be properly exposed: about 147 through VideoToolbox, 65
// through the old tonemap-without-zscale chain, 165 read naively as SDR.
func TestIntegrationPrepareRealHDRSlice(t *testing.T) {
	tools := integrationTools(t)
	slice := os.Getenv("LONGFORM_HDR_SLICE")
	if slice == "" {
		t.Skip("set LONGFORM_HDR_SLICE to a real HDR clip of at least 6 s")
	}
	t.Setenv("LONGFORM_SKIP_TRANSCRIBE", "1")
	src, err := tools.Probe(slice)
	if err != nil {
		t.Fatal(err)
	}
	if !src.HDR() || src.DurationMs < 6000 {
		t.Fatalf("LONGFORM_HDR_SLICE must be HDR and at least 6 s: %+v", src)
	}
	job := stagedJob(t, slice)
	prof := &model.Profile{Name: "test", Inbox: "/tmp", Orientation: "portrait",
		Prepare: model.PrepareConfig{Encoder: model.PrepareEncoderAuto},
		Censor:  model.CensorConfig{Enabled: true, Mode: "beep"}}
	var progress strings.Builder
	started := time.Now()
	if err := Run(job, prof, Options{Tools: tools, Log: &progress}); err != nil {
		t.Fatalf("prepare: %v\nprogress:\n%s", err, progress.String())
	}
	t.Logf("prepare took %s\n%s", time.Since(started).Round(time.Millisecond), progress.String())
	ex, _ := job.Extra["prepare"].(map[string]any)
	t.Logf("extra.prepare tonemap_filter=%v encoder=%v hwaccel=%v; delivery %+v", ex["tonemap_filter"], ex["encoder"], ex["hwaccel"], job.Delivery)
	d := job.Delivery
	if d.ColorTransfer != "bt709" || d.HDR || d.Codec != "h264" || d.Rotation != 0 {
		t.Errorf("delivery not upright H.264 BT.709: %+v", d)
	}
	if (src.Rotation == 90 || src.Rotation == 270) != (d.Height > d.Width) {
		t.Errorf("rotation %d source gave a %dx%d delivery", src.Rotation, d.Width, d.Height)
	}
	y, err := tools.MeanLuma(job.Dir(), DeliveryOut, 5000)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("delivery mean luma at 5 s: %.1f", y)
	if y < 110 || y > 190 {
		t.Errorf("delivery mean luma at 5 s = %.1f, want 110..190", y)
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

// The test binary doubles as a fake ffmpeg and ffprobe: with
// LONGFORM_FFMPEG_STUB=1 it appends its argv to calls.log in
// LONGFORM_FFMPEG_STUB_DIR, prints probe.json from there for ffprobe, lists
// the hwaccels, encoders and filters named (space-separated) in
// LONGFORM_FFMPEG_STUB_{HWACCELS,ENCODERS,FILTERS}, passes the VideoToolbox
// test encode, and fails every other call (an encode), so a test sees what
// prepare would run up to its first encode without media or hardware.
const (
	ffStubEnv    = "LONGFORM_FFMPEG_STUB"
	ffStubDirEnv = "LONGFORM_FFMPEG_STUB_DIR"
)

func TestMain(m *testing.M) {
	if os.Getenv(ffStubEnv) == "1" {
		os.Exit(ffStub(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func ffStub(args []string) int {
	dir := os.Getenv(ffStubDirEnv)
	if dir == "" {
		return 64
	}
	if f, err := os.OpenFile(filepath.Join(dir, "calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	has := func(s string) bool { return indexArg(args, s) >= 0 }
	list := func(env string) []string { return strings.Fields(os.Getenv(env)) }
	switch {
	case has("-show_streams"):
		b, err := os.ReadFile(filepath.Join(dir, "probe.json"))
		if err != nil {
			return 1
		}
		os.Stdout.Write(b)
	case has("-hwaccels"):
		fmt.Println("Hardware acceleration methods:")
		for _, n := range list("LONGFORM_FFMPEG_STUB_HWACCELS") {
			fmt.Println(n)
		}
	case has("-encoders"):
		fmt.Println("Encoders:\n V..... = Video\n ------")
		for _, n := range list("LONGFORM_FFMPEG_STUB_ENCODERS") {
			fmt.Printf(" V....D %-20s stub encoder\n", n)
		}
	case has("-filters"):
		fmt.Println("Filters:\n T.. = Timeline support")
		for _, n := range list("LONGFORM_FFMPEG_STUB_FILTERS") {
			fmt.Printf(" .. %-17s V->V       stub filter\n", n)
		}
	case has("lavfi"): // VideoToolboxCaps' two-frame test encode
	default:
		fmt.Fprintln(os.Stderr, "stub: no encodes here")
		return 1
	}
	return 0
}

func indexArg(args []string, s string) int {
	for i, a := range args {
		if a == s {
			return i
		}
	}
	return -1
}

// A 4K HLG iPhone recording held upright, as ffprobe describes it.
const stubHDRProbe = `{
  "streams": [
    {"index": 0, "codec_name": "hevc", "codec_type": "video", "width": 3840, "height": 2160, "pix_fmt": "yuv420p10le",
     "color_transfer": "arib-std-b67", "color_primaries": "bt2020", "color_space": "bt2020nc",
     "r_frame_rate": "30/1", "avg_frame_rate": "30/1", "duration": "10.000000",
     "side_data_list": [{"side_data_type": "Display Matrix", "rotation": -90}]},
    {"index": 1, "codec_name": "aac", "codec_type": "audio", "sample_rate": "48000", "channels": 2, "channel_layout": "stereo",
     "r_frame_rate": "0/0", "avg_frame_rate": "0/0", "duration": "10.000000"}
  ],
  "format": {"nb_streams": 2, "format_name": "mov,mp4,m4a,3gp,3g2,mj2", "duration": "10.000000"}
}`

// stubJob stages a job whose source the stub ffmpeg describes as HDR, with
// the given ffmpeg capabilities, and returns it with the stub's dir.
func stubJob(t *testing.T, hwaccels, encoders, filters string) (*model.Job, media.Tools, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe.json"), []byte(stubHDRProbe), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ffStubEnv, "1")
	t.Setenv(ffStubDirEnv, dir)
	t.Setenv("LONGFORM_FFMPEG_STUB_HWACCELS", hwaccels)
	t.Setenv("LONGFORM_FFMPEG_STUB_ENCODERS", encoders)
	t.Setenv("LONGFORM_FFMPEG_STUB_FILTERS", filters)
	src := filepath.Join(dir, "IMG_0001.MOV")
	if err := os.WriteFile(src, []byte("not really a movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := stagedJob(t, src)
	return job, media.Tools{FFmpeg: os.Args[0], FFprobe: os.Args[0]}, dir
}

func stubCalls(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// An HDR source on an ffmpeg with neither VideoToolbox's scale_vt nor zscale
// must stop prepare before anything is encoded, say why, and record
// tonemap_filter "unavailable"; a dark tonemap-only delivery from an earlier
// run must not be reused either.
func TestPrepareHDRWithoutConversionFailsLoudly(t *testing.T) {
	// Homebrew ffmpeg 9's filters minus scale_vt, on a Mac whose hardware
	// encoder failed its test (or on Linux, where VideoToolbox is never asked).
	job, tools, dir := stubJob(t, "videotoolbox", "libx264 h264_videotoolbox", "tonemap colorspace deshake transpose_vt")
	// What the removed fallback left behind: a finished, dark remux.
	if err := os.MkdirAll(job.Path("analysis"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job.Path(RemuxOut), []byte("dark"), 0o644); err != nil {
		t.Fatal(err)
	}
	job.Extra = map[string]any{"prepare": map[string]any{"tonemap_filter": "tonemap-only", "hdr": true,
		"steps": map[string]any{"remux": map[string]any{"input": "x", "sig": "tonemap:tonemap-only:h264_videotoolbox", "output": RemuxOut}}}}
	if err := job.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LONGFORM_FFMPEG_STUB_ENCODERS", "libx264") // no hardware encoder
	prof := &model.Profile{Name: "test", Inbox: "/tmp", Prepare: model.PrepareConfig{Encoder: model.PrepareEncoderAuto}}
	var progress strings.Builder
	err := Run(job, prof, Options{Tools: tools, Log: &progress})
	if !errors.Is(err, media.ErrNoHDRConversion) {
		t.Fatalf("want ErrNoHDRConversion, got %v\n%s", err, progress.String())
	}
	if errors.Is(err, media.ErrToolFailed) {
		t.Error("the refusal must not look like an ffmpeg crash")
	}
	msg := err.Error()
	for _, s := range []string{"fixture.mov is HDR (arib-std-b67)", "VideoToolbox", "scale_vt", "zimg", "zscale", "dark"} {
		if !strings.Contains(msg, s) {
			t.Errorf("error lacks %q: %s", s, msg)
		}
	}
	for _, c := range stubCalls(t, dir) {
		if strings.Contains(c, " -vf ") {
			t.Errorf("encoded although no correct conversion exists: %s", c)
		}
	}
	loaded, lerr := model.LoadJob(job.Dir())
	if lerr != nil {
		t.Fatal(lerr)
	}
	ex, _ := loaded.Extra["prepare"].(map[string]any)
	if loaded.State != model.StateStaged || ex["tonemap_filter"] != media.TonemapUnavailable || !strings.Contains(loaded.Error, "is HDR") {
		t.Errorf("job.json: state %s, error %q, extra.prepare %+v", loaded.State, loaded.Error, ex)
	}
	if !strings.Contains(progress.String(), "tonemap     unavailable") {
		t.Errorf("progress lacks the tonemap line:\n%s", progress.String())
	}
}

// On a Mac with VideoToolbox and scale_vt the HDR pass decodes to hardware
// frames, converts and turns them upright on the media engine and encodes
// with h264_videotoolbox, even when prepare.encoder is x264 (which then
// governs the stabilise pass only).
func TestPrepareHDRScaleVTCommand(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("VideoToolbox is macOS only")
	}
	job, tools, dir := stubJob(t, "videotoolbox", "libx264 h264_videotoolbox", "tonemap colorspace deshake scale_vt transpose_vt")
	prof := &model.Profile{Name: "test", Inbox: "/tmp", Prepare: model.PrepareConfig{Encoder: model.PrepareEncoderX264}}
	var progress strings.Builder
	err := Run(job, prof, Options{Tools: tools, Log: &progress})
	if !errors.Is(err, media.ErrToolFailed) || !strings.Contains(err.Error(), "remux") {
		t.Fatalf("want the stub's refusal of the encode, got %v", err)
	}
	var encode string
	for _, c := range stubCalls(t, dir) {
		if strings.Contains(c, " -vf ") {
			encode = c
		}
	}
	for _, want := range []string{
		"-hwaccel videotoolbox -hwaccel_output_format videotoolbox_vld -i ",
		"-vf scale_vt=color_matrix=bt709:color_primaries=bt709:color_transfer=bt709,transpose_vt=dir=clock ",
		"-c:v h264_videotoolbox -q:v 65",
		"-color_primaries bt709 -color_trc bt709 -color_range tv",
	} {
		if !strings.Contains(encode, want) {
			t.Errorf("tonemap command lacks %q:\n%s", want, encode)
		}
	}
	if strings.Contains(encode, "-colorspace") || strings.Contains(encode, "libx264") {
		t.Errorf("tonemap command must not carry -colorspace or libx264:\n%s", encode)
	}
	loaded, lerr := model.LoadJob(job.Dir())
	if lerr != nil {
		t.Fatal(lerr)
	}
	b, _ := json.Marshal(loaded.Extra["prepare"])
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if st.TonemapFilter != media.TonemapScaleVT || st.Encoder != media.EncoderX264 || st.HWAccel != media.HWAccelNone {
		t.Errorf("extra.prepare: tonemap_filter %q encoder %q hwaccel %q, want scale_vt libx264 none", st.TonemapFilter, st.Encoder, st.HWAccel)
	}
	if !strings.Contains(progress.String(), "tonemap     scale_vt (VideoToolbox decode and encode, transpose clock)") {
		t.Errorf("progress:\n%s", progress.String())
	}
}
