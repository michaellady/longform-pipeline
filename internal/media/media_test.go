package media

import (
	"errors"
	"strings"
	"testing"
)

func TestIsHDRTransfer(t *testing.T) {
	cases := map[string]bool{
		"smpte2084": true, "arib-std-b67": true, "SMPTE2084": true, " arib-std-b67 ": true,
		"bt709": false, "unknown": false, "": false, "smpte170m": false, "bt2020-10": false,
	}
	for in, want := range cases {
		if got := IsHDRTransfer(in); got != want {
			t.Errorf("IsHDRTransfer(%q) = %v, want %v", in, got, want)
		}
	}
}

// Captured from ffprobe 8.0.1 on a 3 s testsrc2 clip remuxed with
// -display_rotation 90 (tags/disposition trimmed).
const probeRotated = `{
  "streams": [
    {"index": 0, "codec_name": "h264", "codec_type": "video", "width": 320, "height": 240, "pix_fmt": "yuv420p",
     "color_transfer": "bt709", "color_primaries": "bt709", "color_space": "bt709",
     "r_frame_rate": "30/1", "avg_frame_rate": "30/1", "duration": "3.000000",
     "side_data_list": [{"side_data_type": "Display Matrix", "displaymatrix": "\n00000000:            0      -65536           0\n", "rotation": 90}]},
    {"index": 1, "codec_name": "aac", "codec_type": "audio", "sample_rate": "48000", "channels": 2, "channel_layout": "stereo",
     "r_frame_rate": "0/0", "avg_frame_rate": "0/0", "duration": "3.000000"}
  ],
  "format": {"nb_streams": 2, "format_name": "mov,mp4,m4a,3gp,3g2,mj2", "duration": "3.000000"}
}`

func TestParseProbe(t *testing.T) {
	info, err := ParseProbe([]byte(probeRotated))
	if err != nil {
		t.Fatal(err)
	}
	if info.DurationMs != 3000 || info.Width != 320 || info.Height != 240 || info.FPS != "30/1" {
		t.Errorf("basic fields: %+v", info)
	}
	if info.Rotation != 90 {
		t.Errorf("rotation = %d, want 90", info.Rotation)
	}
	if info.HDR() {
		t.Error("bt709 reported as HDR")
	}
	if !info.HasAudio || info.SampleRate != 48000 || info.Channels != 2 || info.ChannelLayout != "stereo" {
		t.Errorf("audio fields: %+v", info)
	}
	m := info.Model()
	if m.Rotation != 90 || m.Codec != "h264" || m.AudioCodec != "aac" || m.HDR {
		t.Errorf("model: %+v", m)
	}

	hdr := strings.Replace(probeRotated, `"color_transfer": "bt709"`, `"color_transfer": "arib-std-b67"`, 1)
	hdr = strings.Replace(hdr, `"rotation": 90`, `"rotation": -90`, 1)
	info, err = ParseProbe([]byte(hdr))
	if err != nil {
		t.Fatal(err)
	}
	if !info.HDR() || !info.Model().HDR {
		t.Error("HLG not detected as HDR")
	}
	if info.Rotation != 270 {
		t.Errorf("rotation -90 normalised to %d, want 270", info.Rotation)
	}
	if _, err := ParseProbe([]byte(`{"streams":[{"codec_type":"audio"}],"format":{}}`)); err == nil {
		t.Error("expected error without a video stream")
	}
}

func TestZscaleTonemapFilter(t *testing.T) {
	got := ZscaleTonemapFilter("arib-std-b67", "bt2020", "bt2020nc")
	want := "zscale=tin=arib-std-b67:pin=bt2020:min=bt2020nc:t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=yuv420p"
	if got != want {
		t.Errorf("zscale chain:\n got %s\nwant %s", got, want)
	}
	got = ZscaleTonemapFilter("smpte2084", "", "unknown")
	if !strings.HasPrefix(got, "zscale=tin=smpte2084:pin=bt2020:min=bt2020nc:t=linear") {
		t.Errorf("defaults for missing primaries/matrix: %s", got)
	}
	// The tonemap filter must only ever see linearised input.
	if i, j := strings.Index(got, "t=linear"), strings.Index(got, "tonemap="); i < 0 || j < i {
		t.Errorf("tonemap before linearisation: %s", got)
	}
}

func TestVTTranspose(t *testing.T) {
	cases := []struct {
		rotation int
		dir      string
		ok       bool
	}{
		{0, "", true},
		{360, "", true},
		{-90, "clock", true}, // an iPhone held upright; checked on the real slice
		{270, "clock", true}, // Info.Rotation's normalised form of -90
		{90, "cclock", true},
		{-270, "cclock", true},
		{180, "reversal", true},
		{-180, "reversal", true},
		{45, "", false},
		{-30, "", false},
	}
	for _, c := range cases {
		dir, ok := VTTranspose(c.rotation)
		if dir != c.dir || ok != c.ok {
			t.Errorf("VTTranspose(%d) = %q, %v; want %q, %v", c.rotation, dir, ok, c.dir, c.ok)
		}
	}
}

func TestScaleVTTonemapFilter(t *testing.T) {
	const conv = "scale_vt=color_matrix=bt709:color_primaries=bt709:color_transfer=bt709"
	cases := map[int]string{
		0:   conv,
		270: conv + ",transpose_vt=dir=clock",
		-90: conv + ",transpose_vt=dir=clock",
		90:  conv + ",transpose_vt=dir=cclock",
		180: conv + ",transpose_vt=dir=reversal",
	}
	for rot, want := range cases {
		if got := ScaleVTTonemapFilter(rot); got != want {
			t.Errorf("rotation %d:\n got %s\nwant %s", rot, got, want)
		}
	}
	// Frames must stay on the hardware all the way to the encoder.
	for rot := range cases {
		got := ScaleVTTonemapFilter(rot)
		if strings.Contains(got, "hwdownload") || strings.Contains(got, "format=") || strings.Contains(got, "tonemap=") {
			t.Errorf("rotation %d: software step in the hardware chain: %s", rot, got)
		}
	}
}

func TestChooseTonemap(t *testing.T) {
	full := HWCaps{HWAccel: true, Encoder: true, ScaleVT: true, TransposeVT: true}
	x264 := VideoEncode{Encoder: EncoderX264, HWAccel: HWAccelNone, FilterThreads: 6}
	vt := VideoEncode{Encoder: EncoderVideoToolbox, HWAccel: HWAccelVideoToolbox}
	cases := []struct {
		name         string
		goos         string
		enc          VideoEncode
		rotation     int
		caps         HWCaps
		zscale       bool
		want         string
		wantCaps     bool // caps probed
		wantZscale   bool // zscale probed
		wantMissing  string
		wantEncoder  string
		wantHWFrames bool
	}{
		{"mac full portrait", "darwin", vt, 270, full, false, TonemapScaleVT, true, false, "", EncoderVideoToolbox, true},
		{"mac full landscape", "darwin", vt, 0, full, true, TonemapScaleVT, true, false, "", EncoderVideoToolbox, true},
		{"x264 setting still tonemaps on hardware", "darwin", x264, 270, full, true, TonemapScaleVT, true, false, "", EncoderVideoToolbox, true},
		{"no transpose_vt is fine unrotated", "darwin", vt, 0, HWCaps{HWAccel: true, Encoder: true, ScaleVT: true}, false, TonemapScaleVT, true, false, "", EncoderVideoToolbox, true},
		{"no transpose_vt for rotated falls to zscale", "darwin", vt, 90, HWCaps{HWAccel: true, Encoder: true, ScaleVT: true}, true, TonemapZscale, true, true, "", EncoderVideoToolbox, false},
		{"no transpose_vt for rotated, no zscale", "darwin", vt, 90, HWCaps{HWAccel: true, Encoder: true, ScaleVT: true}, false, TonemapUnavailable, true, true, "no transpose_vt", "", false},
		{"odd angle", "darwin", vt, 45, full, false, TonemapUnavailable, true, true, "not a multiple of 90", "", false},
		{"encoder test failed, zscale", "darwin", x264, 270, HWCaps{HWAccel: true, ScaleVT: true, TransposeVT: true}, true, TonemapZscale, true, true, "", EncoderX264, false},
		{"encoder test failed, no zscale", "darwin", x264, 270, HWCaps{HWAccel: true, ScaleVT: true, TransposeVT: true}, false, TonemapUnavailable, true, true, "failed its test encode", "", false},
		{"no hwaccel", "darwin", vt, 0, HWCaps{Encoder: true, ScaleVT: true, TransposeVT: true}, false, TonemapUnavailable, true, true, "no -hwaccel videotoolbox", "", false},
		{"no scale_vt (Homebrew ffmpeg 9 has it; an old one may not)", "darwin", vt, 0, HWCaps{HWAccel: true, Encoder: true, TransposeVT: true}, false, TonemapUnavailable, true, true, "no scale_vt", "", false},
		{"linux zscale", "linux", x264, 270, full, true, TonemapZscale, false, true, "", EncoderX264, false},
		{"linux nothing", "linux", x264, 0, full, false, TonemapUnavailable, false, true, "needs macOS", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var probedCaps, probedZ bool
			plan, err := ChooseTonemap(c.goos, c.enc, c.rotation,
				func() (HWCaps, error) { probedCaps = true; return c.caps, nil },
				func() (bool, error) { probedZ = true; return c.zscale, nil })
			if plan.Filter != c.want {
				t.Fatalf("filter = %q, want %q (err %v)", plan.Filter, c.want, err)
			}
			if probedCaps != c.wantCaps || probedZ != c.wantZscale {
				t.Errorf("probed caps=%v zscale=%v, want %v %v", probedCaps, probedZ, c.wantCaps, c.wantZscale)
			}
			if c.want == TonemapUnavailable {
				if !errors.Is(err, ErrNoHDRConversion) {
					t.Fatalf("want ErrNoHDRConversion, got %v", err)
				}
				msg := err.Error()
				for _, s := range []string{c.wantMissing, "no zscale filter", "VideoToolbox", "scale_vt", "zimg", "zscale", "dark"} {
					if !strings.Contains(msg, s) {
						t.Errorf("message lacks %q: %s", s, msg)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.Encode.EncoderName() != c.wantEncoder || plan.Encode.HWFrames != c.wantHWFrames {
				t.Errorf("encode = %+v, want %s hwframes=%v", plan.Encode, c.wantEncoder, c.wantHWFrames)
			}
			if c.want == TonemapScaleVT {
				if plan.Encode.HWAccelName() != HWAccelVideoToolbox || plan.Encode.FilterThreads != c.enc.FilterThreads {
					t.Errorf("scale_vt must decode on VideoToolbox and keep filter threads: %+v", plan.Encode)
				}
			}
			if c.want == TonemapZscale && plan.Encode != c.enc {
				t.Errorf("zscale must use the profile's encoder %+v, got %+v", c.enc, plan.Encode)
			}
		})
	}
	// A probe failure is an error, not a silent fallback.
	if _, err := ChooseTonemap("darwin", vt, 0, func() (HWCaps, error) { return HWCaps{}, ErrToolFailed }, func() (bool, error) { return true, nil }); !errors.Is(err, ErrToolFailed) {
		t.Errorf("caps error: %v", err)
	}
	if _, err := ChooseTonemap("linux", vt, 0, nil, func() (bool, error) { return false, ErrToolFailed }); !errors.Is(err, ErrToolFailed) {
		t.Errorf("zscale probe error: %v", err)
	}
}

func TestStabilizeFilters(t *testing.T) {
	if got := StabilizeDetectFilter(7, "analysis/transforms.trf"); got != "vidstabdetect=shakiness=7:accuracy=15:result=analysis/transforms.trf" {
		t.Errorf("detect: %s", got)
	}
	if got := StabilizeDetectFilter(0, "x.trf"); got != "vidstabdetect=shakiness=5:accuracy=15:result=x.trf" {
		t.Errorf("detect default: %s", got)
	}
	if got := StabilizeTransformFilter(15, 2, "analysis/transforms.trf"); got != "vidstabtransform=smoothing=15:zoom=2:input=analysis/transforms.trf,unsharp=5:5:0.8:3:3:0.4" {
		t.Errorf("transform: %s", got)
	}
	if got := StabilizeTransformFilter(0, 0, "x.trf"); got != "vidstabtransform=smoothing=10:zoom=0:input=x.trf,unsharp=5:5:0.8:3:3:0.4" {
		t.Errorf("transform default: %s", got)
	}
}

func TestFormatSeconds(t *testing.T) {
	cases := map[int64]string{0: "0.000", 40: "0.040", 1234: "1.234", 61000: "61.000", 999: "0.999", -40: "-0.040"}
	for ms, want := range cases {
		if got := FormatSeconds(ms); got != want {
			t.Errorf("FormatSeconds(%d) = %s, want %s", ms, got, want)
		}
	}
}

func TestAudioFilters(t *testing.T) {
	st := AudioFormat{SampleRate: 48000, Channels: 2, Layout: "stereo"}
	if got := AudioFormatFilter(st); got != "aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo" {
		t.Errorf("aformat: %s", got)
	}
	if got := PanDuplicateFilter(st); got != "pan=stereo|c0=c0|c1=c0" {
		t.Errorf("pan stereo: %s", got)
	}
	mono := AudioFormat{SampleRate: 44100, Channels: 1, Layout: "mono"}
	if got := PanDuplicateFilter(mono); got != "pan=mono|c0=c0" {
		t.Errorf("pan mono: %s", got)
	}
	odd := AudioFormat{SampleRate: 48000, Channels: 3, Layout: ""}
	if got := odd.LayoutToken(); got != "3c" {
		t.Errorf("layout token without name: %s", got)
	}
	empty := (&Info{}).Audio()
	if empty.SampleRate != 48000 || empty.Channels != 2 {
		t.Errorf("audio defaults: %+v", empty)
	}
}

func TestParseFilterList(t *testing.T) {
	out := []byte("Filters:\n T.. = Timeline support\n .S tonemap           V->V       Conversion\n TS zscale            V->V       Apply resizing\n .. vidstabdetect     V->V       Extract\n" +
		" .. scale_vt          V->V       Scale Videotoolbox frames\n .. transpose_vt      V->V       Transpose Videotoolbox frames\n")
	for _, name := range []string{"zscale", "tonemap", "vidstabdetect", "scale_vt", "transpose_vt"} {
		if !ParseFilterList(out, name) {
			t.Errorf("%s not found", name)
		}
	}
	if ParseFilterList(out, "vidstab") || ParseFilterList(out, "scale") {
		t.Error("substring must not match")
	}
}

func TestParseEncoderAndHWAccelLists(t *testing.T) {
	enc := []byte("Encoders:\n V..... = Video\n ------\n V....D libx264              libx264 H.264 / AVC\n V....D h264_videotoolbox    VideoToolbox H.264 Encoder (codec h264)\n")
	if !ParseEncoderList(enc, EncoderVideoToolbox) || !ParseEncoderList(enc, EncoderX264) {
		t.Error("listed encoders not found")
	}
	if ParseEncoderList(enc, "hevc_videotoolbox") || ParseEncoderList(enc, "h264") {
		t.Error("unlisted encoder or substring matched")
	}
	hw := []byte("Hardware acceleration methods:\nvideotoolbox\n\n")
	if !ParseHWAccelList(hw, HWAccelVideoToolbox) {
		t.Error("videotoolbox hwaccel not found")
	}
	if ParseHWAccelList([]byte("Hardware acceleration methods:\n\n"), HWAccelVideoToolbox) || ParseHWAccelList(hw, "video") {
		t.Error("absent or partial hwaccel matched")
	}
}

func TestResolveEncoder(t *testing.T) {
	full := HWCaps{HWAccel: true, Encoder: true}
	cases := []struct {
		name, setting, goos string
		caps                HWCaps
		probeErr            bool
		wantEnc, wantHW     string
		wantErr, wantProbe  bool
	}{
		{"auto darwin full", "auto", "darwin", full, false, EncoderVideoToolbox, HWAccelVideoToolbox, false, true},
		{"empty means auto", "", "darwin", full, false, EncoderVideoToolbox, HWAccelVideoToolbox, false, true},
		{"auto encoder without hwaccel", "auto", "darwin", HWCaps{Encoder: true}, false, EncoderVideoToolbox, HWAccelNone, false, true},
		{"auto encoder unusable", "auto", "darwin", HWCaps{HWAccel: true}, false, EncoderX264, HWAccelNone, false, true},
		{"auto off darwin never probes", "auto", "linux", full, false, EncoderX264, HWAccelNone, false, false},
		{"x264 never probes", "x264", "darwin", full, false, EncoderX264, HWAccelNone, false, false},
		{"explicit videotoolbox", "videotoolbox", "darwin", full, false, EncoderVideoToolbox, HWAccelVideoToolbox, false, true},
		{"explicit videotoolbox unusable", "videotoolbox", "darwin", HWCaps{HWAccel: true}, false, "", "", true, true},
		{"explicit videotoolbox off darwin", "videotoolbox", "linux", HWCaps{}, false, "", "", true, true},
		{"probe error", "auto", "darwin", full, true, "", "", true, true},
		{"bad setting", "nvenc", "darwin", full, false, "", "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probed := false
			probe := func() (HWCaps, error) {
				probed = true
				if c.probeErr {
					return HWCaps{}, ErrToolFailed
				}
				return c.caps, nil
			}
			enc, err := ResolveEncoder(c.setting, c.goos, probe)
			if probed != c.wantProbe {
				t.Errorf("probed = %v, want %v", probed, c.wantProbe)
			}
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", enc)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if enc.EncoderName() != c.wantEnc || enc.HWAccelName() != c.wantHW {
				t.Errorf("got %s/%s, want %s/%s", enc.EncoderName(), enc.HWAccelName(), c.wantEnc, c.wantHW)
			}
		})
	}
}

// indexOf returns the position of the first arg equal to s, or -1.
func indexOf(args []string, s string) int {
	for i, a := range args {
		if a == s {
			return i
		}
	}
	return -1
}

func TestVideoEncodeArgs(t *testing.T) {
	var zero VideoEncode
	if zero.EncoderName() != EncoderX264 || zero.HWAccelName() != HWAccelNone {
		t.Errorf("zero value must be libx264 + software decode: %s %s", zero.EncoderName(), zero.HWAccelName())
	}
	x264 := TonemapArgs("in.mov", "out.mp4", "VF", VideoEncode{Encoder: EncoderX264, HWAccel: HWAccelNone, FilterThreads: 12})
	got := strings.Join(x264, " ")
	want := "-hide_banner -loglevel error -y -filter_threads 12 -i in.mov -map 0:v:0 -map 0:a? -vf VF " +
		"-c:v libx264 -crf 18 -preset medium -threads 0 " +
		"-color_primaries bt709 -color_trc bt709 -colorspace bt709 -color_range tv -c:a aac -b:a 192k -movflags +faststart out.mp4"
	if got != want {
		t.Errorf("x264 tonemap:\n got %s\nwant %s", got, want)
	}

	vt := TonemapArgs("in.mov", "out.mp4", "VF", VideoEncode{Encoder: EncoderVideoToolbox, HWAccel: HWAccelVideoToolbox, FilterThreads: 14})
	got = strings.Join(vt, " ")
	want = "-hide_banner -loglevel error -y -filter_threads 14 -hwaccel videotoolbox -i in.mov -map 0:v:0 -map 0:a? -vf VF " +
		"-c:v h264_videotoolbox -q:v 65 -threads 0 " +
		"-color_primaries bt709 -color_trc bt709 -colorspace bt709 -color_range tv -c:a aac -b:a 192k -movflags +faststart out.mp4"
	if got != want {
		t.Errorf("videotoolbox tonemap:\n got %s\nwant %s", got, want)
	}

	// The scale_vt pass keeps frames on the hardware and leaves -colorspace
	// out (ffmpeg would insert a software scaler the frames cannot enter).
	hw := TonemapArgs("in.mov", "out.mp4", "VF", VideoEncode{Encoder: EncoderVideoToolbox, HWAccel: HWAccelVideoToolbox, HWFrames: true, FilterThreads: 14})
	got = strings.Join(hw, " ")
	want = "-hide_banner -loglevel error -y -filter_threads 14 -hwaccel videotoolbox -hwaccel_output_format videotoolbox_vld -i in.mov -map 0:v:0 -map 0:a? -vf VF " +
		"-c:v h264_videotoolbox -q:v 65 -threads 0 " +
		"-color_primaries bt709 -color_trc bt709 -color_range tv -c:a aac -b:a 192k -movflags +faststart out.mp4"
	if got != want {
		t.Errorf("hardware-frames tonemap:\n got %s\nwant %s", got, want)
	}
	// HWFrames without the VideoToolbox hwaccel adds nothing.
	if sw := (VideoEncode{HWFrames: true}).InputArgs(); indexOf(sw, "-hwaccel_output_format") >= 0 {
		t.Errorf("software decode must not ask for hardware frames: %v", sw)
	}

	st := StabilizeTransformArgs("a.mp4", "b.mp4", "deshake", VideoEncode{Encoder: EncoderVideoToolbox, HWAccel: HWAccelVideoToolbox})
	if i, j := indexOf(st, "-hwaccel"), indexOf(st, "-i"); i < 0 || i > j || st[i+1] != HWAccelVideoToolbox {
		t.Errorf("-hwaccel videotoolbox must precede -i: %v", st)
	}
	if i := indexOf(st, "-filter_threads"); i < 0 || st[i+1] == "0" || st[i+1] == "" {
		t.Errorf("-filter_threads must default to the CPU count: %v", st)
	}
	if i := indexOf(st, "-c:v"); i < 0 || st[i+1] != EncoderVideoToolbox || indexOf(st, "-crf") >= 0 {
		t.Errorf("stabilize must encode with videotoolbox and no CRF: %v", st)
	}
	if i := indexOf(st, "-c:a"); i < 0 || st[i+1] != "copy" || st[len(st)-1] != "b.mp4" {
		t.Errorf("stabilize copies audio and writes the output last: %v", st)
	}
	if indexOf(st, "-hwaccel_output_format") >= 0 {
		t.Errorf("stabilize runs software filters and needs software frames: %v", st)
	}
	if sx := StabilizeTransformArgs("a.mp4", "b.mp4", "deshake", VideoEncode{}); indexOf(sx, "-hwaccel") >= 0 || indexOf(sx, "libx264") < 0 {
		t.Errorf("x264 stabilize must decode in software: %v", sx)
	}
}
