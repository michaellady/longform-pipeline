package media

import (
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

func TestTonemapFilter(t *testing.T) {
	got := TonemapFilter(true, "arib-std-b67", "bt2020", "bt2020nc")
	want := "zscale=tin=arib-std-b67:pin=bt2020:min=bt2020nc:t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=yuv420p"
	if got != want {
		t.Errorf("zscale chain:\n got %s\nwant %s", got, want)
	}
	got = TonemapFilter(true, "smpte2084", "", "unknown")
	if !strings.HasPrefix(got, "zscale=tin=smpte2084:pin=bt2020:min=bt2020nc:t=linear") {
		t.Errorf("defaults for missing primaries/matrix: %s", got)
	}
	got = TonemapFilter(false, "smpte2084", "bt2020", "bt2020nc")
	if !strings.HasPrefix(got, "format=gbrpf32le,tonemap=tonemap=hable:desat=0,format=yuv420p,") ||
		!strings.HasSuffix(got, ",setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=tv") {
		t.Errorf("fallback chain must tonemap then tag BT.709 via setparams: %s", got)
	}
	if strings.Contains(got, "zscale") {
		t.Error("fallback must not reference zscale")
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
	out := []byte("Filters:\n T.. = Timeline support\n .S tonemap           V->V       Conversion\n TS zscale            V->V       Apply resizing\n .. vidstabdetect     V->V       Extract\n")
	for _, name := range []string{"zscale", "tonemap", "vidstabdetect"} {
		if !ParseFilterList(out, name) {
			t.Errorf("%s not found", name)
		}
	}
	if ParseFilterList(out, "vidstab") || ParseFilterList(out, "scale") {
		t.Error("substring must not match")
	}
}
