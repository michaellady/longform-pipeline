package media

import (
	"fmt"
	"strings"
)

// Pure filtergraph builders. Paths passed to these are used inside filter
// argument strings, where ':' ',' and '\' are special; callers pass paths
// relative to the job dir (ffmpeg runs with cwd = job dir) so no escaping is
// needed for the fixed layout analysis/transforms.trf.

// TonemapFilter builds the HDR -> BT.709 SDR chain. With zscale (the normal
// case) the input transfer/primaries/matrix are passed explicitly so an
// untagged frame cannot make zscale fail with "no path between colorspaces";
// bt2020 is the only primaries set an iPhone HDR file uses. Without zscale the
// fallback linearises via format conversion and tonemaps directly, which is
// less accurate but keeps the pipeline moving.
func TonemapFilter(useZscale bool, transfer, primaries, matrix string) string {
	if !useZscale {
		return "format=gbrpf32le,tonemap=tonemap=hable:desat=0,format=yuv420p"
	}
	if primaries == "" || primaries == "unknown" {
		primaries = "bt2020"
	}
	if matrix == "" || matrix == "unknown" {
		matrix = "bt2020nc"
	}
	in := ""
	if transfer != "" && transfer != "unknown" {
		in = fmt.Sprintf("tin=%s:pin=%s:min=%s:", transfer, primaries, matrix)
	}
	return "zscale=" + in + "t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=yuv420p"
}

// Vidstab defaults when the profile leaves a value at zero.
const (
	DefaultShakiness = 5
	DefaultSmoothing = 10
)

// StabilizeDetectFilter is vidstab pass 1. trf is relative to the job dir.
func StabilizeDetectFilter(shakiness int, trf string) string {
	if shakiness <= 0 {
		shakiness = DefaultShakiness
	}
	return fmt.Sprintf("vidstabdetect=shakiness=%d:accuracy=15:result=%s", shakiness, trf)
}

// StabilizeTransformFilter is vidstab pass 2 plus the customary unsharp.
func StabilizeTransformFilter(smoothing, zoom int, trf string) string {
	if smoothing <= 0 {
		smoothing = DefaultSmoothing
	}
	return fmt.Sprintf("vidstabtransform=smoothing=%d:zoom=%d:input=%s,unsharp=5:5:0.8:3:3:0.4", smoothing, zoom, trf)
}

// FormatSeconds renders milliseconds as fixed 3-decimal seconds for filter
// expressions (deterministic, locale-free).
func FormatSeconds(ms int64) string {
	neg := ""
	if ms < 0 {
		neg = "-"
		ms = -ms
	}
	return fmt.Sprintf("%s%d.%03d", neg, ms/1000, ms%1000)
}

// AudioFormatFilter pins sample format, rate and layout so every branch of a
// mix matches exactly.
func AudioFormatFilter(af AudioFormat) string {
	return fmt.Sprintf("aformat=sample_fmts=fltp:sample_rates=%d:channel_layouts=%s", af.SampleRate, af.LayoutToken())
}

// PanDuplicateFilter copies channel 0 of a mono source to every output
// channel at unity gain. (A plain aformat upmix from mono applies sqrt(1/2),
// which would land a -12 dBFS beep at -15 dBFS.)
func PanDuplicateFilter(af AudioFormat) string {
	var b strings.Builder
	b.WriteString("pan=")
	b.WriteString(af.LayoutToken())
	for c := 0; c < af.Channels; c++ {
		fmt.Fprintf(&b, "|c%d=c0", c)
	}
	return b.String()
}
