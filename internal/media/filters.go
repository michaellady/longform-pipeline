package media

import (
	"fmt"
	"strings"
)

// Pure filtergraph builders. Paths passed to these are used inside filter
// argument strings, where ':' ',' and '\' are special; callers pass paths
// relative to the job dir (ffmpeg runs with cwd = job dir) so no escaping is
// needed for the fixed layout analysis/transforms.trf.

// ZscaleTonemapFilter builds the software HDR -> BT.709 SDR chain: zscale
// linearises the HDR transfer, tonemap (hable) compresses the range and
// zscale converts to BT.709. The input transfer/primaries/matrix are passed
// explicitly so an untagged frame cannot make zscale fail with "no path
// between colorspaces"; bt2020 is the only primaries set an iPhone HDR file
// uses. The tonemap filter needs that linear input: run on the HLG or PQ
// signal itself it makes a badly underexposed picture, which is why there is
// no chain without zscale.
func ZscaleTonemapFilter(transfer, primaries, matrix string) string {
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

// ScaleVTTonemapFilter builds the VideoToolbox HDR -> BT.709 SDR chain:
// scale_vt converts hardware frames (decoded with -hwaccel videotoolbox and
// -hwaccel_output_format videotoolbox_vld) to BT.709 with Apple's own HDR
// conversion, and transpose_vt turns them upright. Hardware frames bypass
// ffmpeg's autorotate, so without the transpose a portrait iPhone recording
// would come out sideways with no rotation tag. The frames stay on the
// hardware into h264_videotoolbox: an hwdownload in this chain makes that
// encoder fail (-22).
func ScaleVTTonemapFilter(rotation int) string {
	vf := "scale_vt=color_matrix=bt709:color_primaries=bt709:color_transfer=bt709"
	if dir, ok := VTTranspose(rotation); ok && dir != "" {
		vf += ",transpose_vt=dir=" + dir
	}
	return vf
}

// VTTranspose is the transpose_vt direction that turns a frame upright, the
// hardware twin of ffmpeg's autorotate. rotation is the display-matrix
// rotation as ffprobe reports it (Info.Rotation normalises it to [0,360)):
// -90 (270, an iPhone held upright) -> clock, 90 -> cclock, 180 -> reversal,
// 0 -> "" (no transpose). Each was checked on HEVC HLG frames against the
// autorotated software decode (SSIM 0.96 for the match, under 0.46 for every
// other direction); reversal equals clock twice in one pass. ok is false for
// an angle that is not a multiple of 90, which transpose_vt cannot apply.
func VTTranspose(rotation int) (dir string, ok bool) {
	switch ((rotation % 360) + 360) % 360 {
	case 0:
		return "", true
	case 90:
		return "cclock", true
	case 180:
		return "reversal", true
	case 270:
		return "clock", true
	}
	return "", false
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
