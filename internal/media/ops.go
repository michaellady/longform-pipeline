package media

import (
	"fmt"
	"regexp"
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

// Tonemap re-encodes HDR video to BT.709 SDR with the given -vf chain. ffmpeg's
// autorotate (default on) applies the display matrix during decode, so the
// output is upright without a rotation tag.
func (t Tools) Tonemap(dir, in, out, vf string) error {
	// The output is always tagged BT.709 SDR. With zscale the filter chain
	// already converts to it; the tonemap-only fallback (an ffmpeg without
	// zscale) approximates the conversion, and without these tags the file
	// would still advertise the HDR transfer it no longer carries.
	_, err := t.run(dir, t.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", in,
		"-map", "0:v:0", "-map", "0:a?",
		"-vf", vf, "-c:v", "libx264", "-crf", "18", "-preset", "medium",
		"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv",
		"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", out)
	return err
}

// StabilizeDetect is vidstab pass 1 (writes the .trf named inside vf).
func (t Tools) StabilizeDetect(dir, in, vf string) error {
	_, err := t.run(dir, t.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", in,
		"-vf", vf, "-an", "-f", "null", "-")
	return err
}

// StabilizeTransform is vidstab pass 2; audio is copied untouched.
func (t Tools) StabilizeTransform(dir, in, out, vf string) error {
	_, err := t.run(dir, t.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", in,
		"-map", "0:v:0", "-map", "0:a?",
		"-vf", vf, "-c:v", "libx264", "-crf", "18", "-preset", "medium",
		"-c:a", "copy", "-movflags", "+faststart", out)
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

type captureLog struct{ b []byte }

func (c *captureLog) Write(p []byte) (int, error) { c.b = append(c.b, p...); return len(p), nil }
func (c *captureLog) String() string              { return string(c.b) }
