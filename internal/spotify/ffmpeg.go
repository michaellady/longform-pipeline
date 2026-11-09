package spotify

import (
	"fmt"

	"github.com/michaellady/longform-pipeline/internal/media"
)

// Episode output geometry: Spotify video podcasts are 16:9 widescreen.
const (
	OutWidth  = 1920
	OutHeight = 1080
	OutRate   = "30"
)

// Is16x9 reports whether a display size is widescreen 16:9 within 1%.
func Is16x9(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	d := w*9 - h*16
	if d < 0 {
		d = -d
	}
	return d*100 <= h*16
}

// DisplaySize is the frame size a player shows: width and height swapped
// for a 90/270-degree rotation (ffmpeg autorotates on decode).
func DisplaySize(info *media.Info) (w, h int) {
	if info.Rotation == 90 || info.Rotation == 270 {
		return info.Height, info.Width
	}
	return info.Width, info.Height
}

// PillarboxFilter is the filter_complex that puts a non-16:9 frame on a
// 1920x1080 canvas over a blurred, darkened, cropped copy of itself. A frame
// narrower than 16:9 (the vertical OpusClip export) is pillarboxed: scaled to
// 1080 high and centred. A frame wider than 16:9 is letterboxed: scaled to
// 1920 wide and centred. The output pad is [v].
func PillarboxFilter(srcW, srcH int) string {
	bg := fmt.Sprintf("[a]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,boxblur=40:2,eq=brightness=-0.12[bg]", OutWidth, OutHeight, OutWidth, OutHeight)
	fg := fmt.Sprintf("[b]scale=-2:%d[fg];[bg][fg]overlay=(W-w)/2:0", OutHeight)
	if srcW*OutHeight > srcH*OutWidth { // wider than 16:9
		fg = fmt.Sprintf("[b]scale=%d:-2[fg];[bg][fg]overlay=0:(H-h)/2", OutWidth)
	}
	return "[0:v]split[a][b];" + bg + ";" + fg + ",format=yuv420p[v]"
}

// VideoCodecArgs are the H.264 High encoder options for the episode:
// h264_videotoolbox at 8 Mb/s (12 Mb/s peak), the command verified on the
// first episode, or libx264 CRF 18 capped at the same peak.
func VideoCodecArgs(encoder string) []string {
	if encoder == media.EncoderVideoToolbox {
		return []string{"-c:v", media.EncoderVideoToolbox, "-profile:v", "high", "-b:v", "8M", "-maxrate", "12M"}
	}
	return []string{"-c:v", media.EncoderX264, "-profile:v", "high", "-preset", "medium", "-crf", "18", "-maxrate", "12M", "-bufsize", "24M"}
}

// AudioCodecArgs copy AAC audio and re-encode anything else to AAC.
func AudioCodecArgs(audioCodec string) []string {
	if audioCodec == "aac" {
		return []string{"-c:a", "copy"}
	}
	return []string{"-c:a", "aac", "-b:a", "192k"}
}

// EncodeArgs is the ffmpeg command line that turns the source into the
// episode file. A 16:9 H.264 source is stream-copied (remux with faststart);
// any other 16:9 source is re-encoded at 1080p; a source of any other shape
// is pillarboxed (or letterboxed) onto a blurred 1920x1080 fill. reencode
// reports whether the video is re-encoded.
func EncodeArgs(in, out string, info *media.Info, encoder string) (args []string, reencode bool) {
	w, h := DisplaySize(info)
	args = []string{"-hide_banner", "-loglevel", "error", "-y", "-i", in}
	switch {
	case Is16x9(w, h) && info.VideoCodec == "h264":
		args = append(args, "-map", "0:v:0", "-map", "0:a?", "-c:v", "copy")
	case Is16x9(w, h):
		reencode = true
		args = append(args, "-map", "0:v:0", "-map", "0:a?", "-vf", fmt.Sprintf("scale=%d:%d,format=yuv420p", OutWidth, OutHeight))
		args = append(args, VideoCodecArgs(encoder)...)
		args = append(args, "-r", OutRate)
	default:
		reencode = true
		args = append(args, "-filter_complex", PillarboxFilter(w, h), "-map", "[v]", "-map", "0:a?")
		args = append(args, VideoCodecArgs(encoder)...)
		args = append(args, "-r", OutRate)
	}
	args = append(args, AudioCodecArgs(info.AudioCodec)...)
	return append(args, "-movflags", "+faststart", out), reencode
}
