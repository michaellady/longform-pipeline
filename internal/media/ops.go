package media

import (
	"fmt"
	"regexp"
	"runtime"
	"strconv"
)

// All ops run ffmpeg with cwd = dir so filter-side paths (the vidstab .trf)
// can be job-relative and need no filter escaping. Input/output paths may be
// absolute or dir-relative.

// Remux copies streams into a faststart mp4. Rotation side data survives a
// stream copy, so a portrait recording stays upright for players.
func (t Tools) Remux(dir, in, out string) error {
	_, err := t.run(dir, t.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", in,
		"-map", "0:v:0", "-map", "0:a?", "-c", "copy", "-movflags", "+faststart", out)
	return err
}

// Encoder and hwaccel names as ffmpeg spells them; prepare records them in
// job.extra.prepare.
const (
	EncoderX264         = "libx264"
	EncoderVideoToolbox = "h264_videotoolbox"
	HWAccelVideoToolbox = "videotoolbox"
	HWAccelNone         = "none"
)

// VideoToolboxQuality is h264_videotoolbox's constant-quality setting
// (-q:v, 1..100). On a 10 s slice of a 4K HLG iPhone recording, 65 with
// hardware decode tonemapped about 2.7x faster than libx264 CRF 18 medium,
// wrote under a third of the bytes, and scored SSIM 0.99 (luma) against it.
const VideoToolboxQuality = "65"

// VideoEncode is how a re-encoding op decodes and encodes video. The zero
// value is software decode plus libx264 CRF 18 medium.
type VideoEncode struct {
	Encoder       string // EncoderX264 (or "") | EncoderVideoToolbox
	HWAccel       string // "" or HWAccelNone = software decode | HWAccelVideoToolbox
	FilterThreads int    // -filter_threads; <= 0 = runtime.NumCPU()
	// HWFrames keeps decoded frames on the media engine
	// (-hwaccel_output_format videotoolbox_vld) for the *_vt filters and the
	// hardware encoder. Only the scale_vt HDR pass sets it: those frames skip
	// ffmpeg's autorotate, and software filters cannot read them.
	HWFrames bool
	// Rate is the constant output frame rate, the source's exact rate as
	// ffprobe gives it ("30/1"); "" leaves the rate to ffmpeg. Every encode
	// is constant frame rate either way (RateArgs).
	Rate string
}

// EncoderName is the ffmpeg encoder this setting runs.
func (e VideoEncode) EncoderName() string {
	if e.Encoder == "" {
		return EncoderX264
	}
	return e.Encoder
}

// HWAccelName is the decode hwaccel this setting runs, HWAccelNone for
// software decode.
func (e VideoEncode) HWAccelName() string {
	if e.HWAccel == "" {
		return HWAccelNone
	}
	return e.HWAccel
}

// InputArgs go before -i. -filter_threads sizes the filtergraph's thread
// pool. -hwaccel decodes on Apple's media engine; without
// -hwaccel_output_format the frames are copied back to system memory for the
// software filters (zscale, deshake), and a stream the engine cannot decode
// falls back to software decode. With HWFrames they stay on the engine.
func (e VideoEncode) InputArgs() []string {
	n := e.FilterThreads
	if n <= 0 {
		n = runtime.NumCPU()
	}
	args := []string{"-filter_threads", strconv.Itoa(n)}
	if h := e.HWAccelName(); h != HWAccelNone {
		args = append(args, "-hwaccel", h)
		if e.HWFrames && h == HWAccelVideoToolbox {
			args = append(args, "-hwaccel_output_format", "videotoolbox_vld")
		}
	}
	return args
}

// CodecArgs select and configure the video encoder (output options).
func (e VideoEncode) CodecArgs() []string {
	if e.EncoderName() == EncoderVideoToolbox {
		return []string{"-c:v", EncoderVideoToolbox, "-q:v", VideoToolboxQuality, "-threads", "0"}
	}
	return []string{"-c:v", EncoderX264, "-crf", "18", "-preset", "medium", "-threads", "0"}
}

// RateArgs make the output constant frame rate (output options). By default
// ffmpeg writes an mp4 at variable rate, rounding each frame's time to the
// output grid and keeping whatever gaps that leaves: a 24-minute iPhone
// recording (43368 frames whose 1/600 s timestamp jitter adds up to two frame
// times) came out of the VideoToolbox passes with two packets held for two
// frames (avg_frame_rate 2265/76), and OpusClip's full-video render of it
// broke. -fps_mode cfr repeats a frame where the timeline runs ahead instead
// (and drops one where frames crowd); -r pins the grid to the source rate.
// Both work on hardware frames: ffmpeg 9 still hands videotoolbox_vld frames
// straight to h264_videotoolbox.
func (e VideoEncode) RateArgs() []string {
	args := []string{"-fps_mode", "cfr"}
	if e.Rate != "" {
		args = append(args, "-r", e.Rate)
	}
	return args
}

// encodeArgs assembles one decode-filter-encode run: video 0 and any audio
// mapped, vf applied, video encoded per enc at a constant frame rate, then
// the trailing output args.
func encodeArgs(in, out, vf string, enc VideoEncode, tailArgs ...string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	args = append(args, enc.InputArgs()...)
	args = append(args, "-i", in, "-map", "0:v:0", "-map", "0:a?", "-vf", vf)
	args = append(args, enc.CodecArgs()...)
	args = append(args, enc.RateArgs()...)
	args = append(args, tailArgs...)
	return append(args, out)
}

// TonemapArgs is the ffmpeg command line Tonemap runs (exported for tests).
func TonemapArgs(in, out, vf string, enc VideoEncode) []string {
	// The output is always tagged BT.709 SDR: the chain has converted to it,
	// and the tags keep the file from advertising the HDR transfer it no
	// longer carries. On hardware frames -colorspace is left out: ffmpeg 9
	// negotiates the matrix inside the filtergraph and would insert a software
	// scaler, which cannot take videotoolbox_vld frames, so the encoder never
	// opens. scale_vt has already tagged those frames BT.709.
	tags := []string{"-color_primaries", "bt709", "-color_trc", "bt709"}
	if !enc.HWFrames {
		tags = append(tags, "-colorspace", "bt709")
	}
	tags = append(tags, "-color_range", "tv", "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart")
	return encodeArgs(in, out, vf, enc, tags...)
}

// Tonemap re-encodes HDR video to BT.709 SDR with the given -vf chain. On
// software frames ffmpeg's autorotate (default on) applies the display matrix
// during decode; hardware frames skip it, so the scale_vt chain carries its
// own transpose_vt. Either way the output is upright without a rotation tag.
func (t Tools) Tonemap(dir, in, out, vf string, enc VideoEncode) error {
	_, err := t.run(dir, t.FFmpeg, TonemapArgs(in, out, vf, enc)...)
	return err
}

// StabilizeDetect is vidstab pass 1 (writes the .trf named inside vf).
func (t Tools) StabilizeDetect(dir, in, vf string) error {
	_, err := t.run(dir, t.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", in,
		"-vf", vf, "-an", "-f", "null", "-")
	return err
}

// StabilizeTransformArgs is the ffmpeg command line StabilizeTransform runs.
func StabilizeTransformArgs(in, out, vf string, enc VideoEncode) []string {
	return encodeArgs(in, out, vf, enc, "-c:a", "copy", "-movflags", "+faststart")
}

// StabilizeTransform is vidstab pass 2 (or the single deshake pass); audio is
// copied untouched.
func (t Tools) StabilizeTransform(dir, in, out, vf string, enc VideoEncode) error {
	_, err := t.run(dir, t.FFmpeg, StabilizeTransformArgs(in, out, vf, enc)...)
	return err
}

// ExtractFrame writes one JPEG from the frame at atMs.
func (t Tools) ExtractFrame(dir, in string, atMs int64, out string) error {
	_, err := t.run(dir, t.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-ss", FormatSeconds(atMs), "-i", in, "-frames:v", "1", "-q:v", "2", out)
	return err
}

// ExtractWav16k writes the 16 kHz mono PCM wav whisper.cpp expects.
func (t Tools) ExtractWav16k(dir, in, out string) error {
	_, err := t.run(dir, t.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", in,
		"-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", out)
	return err
}

// RenderAudioGraph re-encodes only the audio through a filter_complex graph
// that must label its output [aout]; video is stream-copied.
func (t Tools) RenderAudioGraph(dir, in, out, graph string) error {
	_, err := t.run(dir, t.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", in,
		"-filter_complex", graph, "-map", "0:v:0", "-map", "[aout]",
		"-c:v", "copy", "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", out)
	return err
}

var maxVolumeRe = regexp.MustCompile(`max_volume:\s*(-?[0-9.]+) dB`)

// MaxVolumeDB measures peak level with volumedetect (used by tests to verify
// the beep lands at -12 dBFS).
func (t Tools) MaxVolumeDB(dir, in string) (float64, error) {
	// volumedetect reports on stderr, which run() only returns on failure, so
	// capture it via a tee into a throwaway logger.
	var cap captureLog
	tt := Tools{FFmpeg: t.FFmpeg, FFprobe: t.FFprobe, Log: &cap}
	if _, err := tt.run(dir, t.FFmpeg, "-hide_banner", "-i", in, "-vn", "-af", "volumedetect", "-f", "null", "-"); err != nil {
		return 0, err
	}
	m := maxVolumeRe.FindStringSubmatch(cap.String())
	if m == nil {
		return 0, fmt.Errorf("volumedetect: no max_volume in output")
	}
	return strconv.ParseFloat(m[1], 64)
}

var yavgRe = regexp.MustCompile(`lavfi\.signalstats\.YAVG=([0-9.]+)`)

// MeanLuma is the mean 8-bit luma (signalstats YAVG on a 0..255 scale) of
// the frame at atMs, decoded in software (so autorotated) and converted to
// yuv420p. Tests use it as a brightness check on tonemapped output.
func (t Tools) MeanLuma(dir, in string, atMs int64) (float64, error) {
	var cap captureLog
	tt := Tools{FFmpeg: t.FFmpeg, FFprobe: t.FFprobe, Log: &cap}
	if _, err := tt.run(dir, t.FFmpeg, "-hide_banner", "-ss", FormatSeconds(atMs), "-i", in, "-frames:v", "1",
		"-vf", "format=yuv420p,signalstats,metadata=print:key=lavfi.signalstats.YAVG", "-f", "null", "-"); err != nil {
		return 0, err
	}
	m := yavgRe.FindStringSubmatch(cap.String())
	if m == nil {
		return 0, fmt.Errorf("signalstats: no YAVG in output")
	}
	return strconv.ParseFloat(m[1], 64)
}

type captureLog struct{ b []byte }

func (c *captureLog) Write(p []byte) (int, error) { c.b = append(c.b, p...); return len(p), nil }
func (c *captureLog) String() string              { return string(c.b) }
