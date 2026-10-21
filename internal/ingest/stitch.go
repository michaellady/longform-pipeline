package ingest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// StitchResult records how the concatenation was produced.
type StitchResult struct {
	Mode       string // "copy" (concat demuxer, -c copy) or "reencode" (libx264 crf 18 + aac)
	DurationMs int64
}

// Stitch concatenates inputs (already in recording order) into out. It tries
// the lossless concat demuxer first and re-encodes when the inputs differ in
// codec or geometry, when ffmpeg fails, or when the copied output's duration
// does not add up.
func Stitch(inputs []string, out string, logf func(string, ...any)) (*StitchResult, error) {
	if len(inputs) == 0 {
		return nil, errors.New("no inputs")
	}
	ffmpeg := LookPath("ffmpeg")
	if ffmpeg == "" {
		return nil, ErrNoFFmpeg
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	probes := make([]*ProbeResult, len(inputs))
	var expected int64
	uniform, probed := true, true
	for i, in := range inputs {
		pr, err := Probe(in)
		if err != nil {
			probed = false
			break
		}
		probes[i] = pr
		expected += pr.Probe.DurationMs
		if i > 0 && !sameStreams(probes[0], pr) {
			uniform = false
		}
	}
	if !probed || uniform {
		if err := concatCopy(ffmpeg, inputs, out); err != nil {
			logf("stitch: -c copy failed (%v); re-encoding", err)
		} else if !probed {
			return &StitchResult{Mode: "copy"}, nil
		} else if got, err := durationOf(out); err != nil {
			logf("stitch: cannot probe copied output (%v); re-encoding", err)
		} else if diff := got - expected; diff > tolerance(expected) || -diff > tolerance(expected) {
			logf("stitch: -c copy output is %d ms, inputs sum to %d ms; re-encoding", got, expected)
		} else {
			return &StitchResult{Mode: "copy", DurationMs: got}, nil
		}
		os.Remove(out)
	} else {
		logf("stitch: inputs differ in codec or geometry; re-encoding")
	}
	if !probed {
		return nil, errors.New("ffprobe unavailable; cannot plan a re-encode")
	}
	if err := concatReencode(ffmpeg, inputs, probes, out); err != nil {
		os.Remove(out)
		return nil, err
	}
	got, err := durationOf(out)
	if err != nil {
		return nil, fmt.Errorf("re-encoded output: %w", err)
	}
	return &StitchResult{Mode: "reencode", DurationMs: got}, nil
}

func tolerance(expectedMs int64) int64 { return max(int64(1000), expectedMs/50) }

func sameStreams(a, b *ProbeResult) bool {
	return a.Probe.Codec == b.Probe.Codec && a.Probe.Width == b.Probe.Width && a.Probe.Height == b.Probe.Height &&
		a.Probe.FPS == b.Probe.FPS && a.Probe.AudioCodec == b.Probe.AudioCodec && a.HasAudio == b.HasAudio &&
		a.Probe.Rotation == b.Probe.Rotation && a.Probe.ColorTransfer == b.Probe.ColorTransfer
}

func durationOf(path string) (int64, error) {
	pr, err := Probe(path)
	if err != nil {
		return 0, err
	}
	return pr.Probe.DurationMs, nil
}

func runFFmpeg(ffmpeg string, args []string) error {
	cmd := exec.Command(ffmpeg, append([]string{"-y", "-hide_banner", "-nostdin", "-loglevel", "error"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		return fmt.Errorf("ffmpeg: %v: %s", err, msg)
	}
	return nil
}

func concatCopy(ffmpeg string, inputs []string, out string) error {
	list := out + ".concat.txt"
	var b strings.Builder
	for _, in := range inputs {
		fmt.Fprintf(&b, "file '%s'\n", strings.ReplaceAll(in, "'", `'\''`))
	}
	if err := os.WriteFile(list, []byte(b.String()), 0o644); err != nil {
		return err
	}
	defer os.Remove(list)
	return runFFmpeg(ffmpeg, []string{"-f", "concat", "-safe", "0", "-i", list, "-c", "copy", "-movflags", "+faststart", out})
}

// concatReencode normalises every input to the first input's display
// geometry and frame rate, then concatenates with the concat filter.
func concatReencode(ffmpeg string, inputs []string, probes []*ProbeResult, out string) error {
	first := probes[0].Probe
	w, h := first.Width, first.Height
	if r := first.Rotation % 180; r == 90 || r == -90 {
		w, h = h, w // ffmpeg autorotates on decode; match the displayed frame
	}
	if w == 0 || h == 0 {
		return errors.New("first input has no geometry")
	}
	fps := first.FPS
	if fps == "" {
		fps = "30"
	}
	audio := true
	for _, p := range probes {
		if !p.HasAudio {
			audio = false
		}
	}
	var fc strings.Builder
	for i := range inputs {
		fmt.Fprintf(&fc, "[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%s,format=yuv420p[v%d];", i, w, h, w, h, fps, i)
		if audio {
			fmt.Fprintf(&fc, "[%d:a]aresample=48000,aformat=channel_layouts=stereo[a%d];", i, i)
		}
	}
	for i := range inputs {
		fmt.Fprintf(&fc, "[v%d]", i)
		if audio {
			fmt.Fprintf(&fc, "[a%d]", i)
		}
	}
	na := 0
	if audio {
		na = 1
	}
	fmt.Fprintf(&fc, "concat=n=%d:v=1:a=%d[v]", len(inputs), na)
	if audio {
		fc.WriteString("[a]")
	}
	args := []string{}
	for _, in := range inputs {
		args = append(args, "-i", in)
	}
	args = append(args, "-filter_complex", fc.String(), "-map", "[v]")
	if audio {
		args = append(args, "-map", "[a]", "-c:a", "aac", "-b:a", "192k")
	}
	args = append(args, "-c:v", "libx264", "-crf", "18", "-preset", "medium", "-movflags", "+faststart", out)
	return runFFmpeg(ffmpeg, args)
}
