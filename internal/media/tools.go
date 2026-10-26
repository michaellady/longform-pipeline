// Package media wraps ffmpeg and ffprobe for the prepare stage: probing,
// remuxing, HDR tonemapping, vidstab stabilisation, frame grabs, audio
// extraction and the censor render. Filtergraphs are built by pure functions
// (filters.go) so they are unit-testable without media.
package media

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// Tools holds the resolved ffmpeg/ffprobe binaries and an optional log sink
// that receives every command line and its full stderr.
type Tools struct {
	FFmpeg  string
	FFprobe string
	Log     io.Writer
}

// FindTools resolves ffmpeg and ffprobe from PATH, falling back to the
// Homebrew prefix. It returns an error naming the missing binary.
func FindTools() (Tools, error) {
	var t Tools
	var err error
	if t.FFmpeg, err = findBinary("ffmpeg"); err != nil {
		return t, err
	}
	if t.FFprobe, err = findBinary("ffprobe"); err != nil {
		return t, err
	}
	return t, nil
}

func findBinary(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/" + name, "/usr/local/bin/" + name} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found on PATH or in /opt/homebrew/bin", name)
}

// ErrToolFailed wraps a non-zero exit from ffmpeg/ffprobe.
var ErrToolFailed = errors.New("media tool failed")

// run executes a tool with cwd=dir, logs the command and its stderr, and on
// failure returns the tail of stderr so callers get a readable cause.
func (t Tools) run(dir, bin string, args ...string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if t.Log != nil {
		fmt.Fprintf(t.Log, "$ %s %s\n", bin, strings.Join(args, " "))
	}
	err := cmd.Run()
	if t.Log != nil && stderr.Len() > 0 {
		t.Log.Write(stderr.Bytes())
		if !bytes.HasSuffix(stderr.Bytes(), []byte("\n")) {
			io.WriteString(t.Log, "\n")
		}
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%w: %s %s: %v\n%s", ErrToolFailed, bin, strings.Join(args, " "), err, tail(stderr.String(), 15))
	}
	return stdout.Bytes(), nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// HasFilter reports whether the local ffmpeg lists a filter by name.
func (t Tools) HasFilter(name string) (bool, error) {
	out, err := t.run("", t.FFmpeg, "-hide_banner", "-filters")
	if err != nil {
		return false, err
	}
	return ParseFilterList(out, name), nil
}

// ParseFilterList scans `ffmpeg -filters` output for a filter name. Lines look
// like " TS zscale           V->V       Apply resizing...".
func ParseFilterList(out []byte, name string) bool { return parseNameColumn(out, name) }

// ParseEncoderList scans `ffmpeg -encoders` output for an encoder name. Lines
// look like " V....D h264_videotoolbox    VideoToolbox H.264 Encoder ...".
func ParseEncoderList(out []byte, name string) bool { return parseNameColumn(out, name) }

// parseNameColumn matches the second whitespace field of any line, the name
// column of ffmpeg's -filters and -encoders listings.
func parseNameColumn(out []byte, name string) bool {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[1] == name {
			return true
		}
	}
	return false
}

// ParseHWAccelList scans `ffmpeg -hwaccels` output, a header line followed by
// one method name per line ("Hardware acceleration methods:\nvideotoolbox").
func ParseHWAccelList(out []byte, name string) bool {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == name {
			return true
		}
	}
	return false
}

// HWCaps is what the local ffmpeg can do on Apple's media engine.
type HWCaps struct {
	HWAccel bool // `ffmpeg -hwaccels` lists videotoolbox (hardware decode)
	Encoder bool // h264_videotoolbox is listed and passed a test encode
}

// VideoToolboxCaps probes the real ffmpeg. Being listed is not enough for the
// encoder: it must also encode two test frames with the exact options prepare
// uses, because ffmpeg accepts -q:v for VideoToolbox only on Apple Silicon and
// the encoder requires the hardware engine by default, which a VM (a CI
// runner) may not expose. A failed test encode is a "no", not an error.
func (t Tools) VideoToolboxCaps() (HWCaps, error) {
	var c HWCaps
	out, err := t.run("", t.FFmpeg, "-hide_banner", "-hwaccels")
	if err != nil {
		return c, err
	}
	c.HWAccel = ParseHWAccelList(out, HWAccelVideoToolbox)
	out, err = t.run("", t.FFmpeg, "-hide_banner", "-encoders")
	if err != nil {
		return c, err
	}
	if ParseEncoderList(out, EncoderVideoToolbox) {
		args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=256x256:rate=30", "-frames:v", "2"}
		args = append(args, VideoEncode{Encoder: EncoderVideoToolbox}.CodecArgs()...)
		args = append(args, "-f", "null", "-")
		_, err := t.run("", t.FFmpeg, args...)
		c.Encoder = err == nil
	}
	return c, nil
}

// ResolveEncoder turns the profile's prepare.encoder setting into the
// VideoEncode the re-encoding ops use. probe is called only when the answer
// depends on it: "auto" uses VideoToolbox (hardware decode too, when listed)
// on darwin when the encoder passed its test, else libx264; "videotoolbox"
// fails when it did not; "x264" is today's libx264 path without hardware
// decode. goos is runtime.GOOS (a parameter so tests can vary it).
func ResolveEncoder(setting, goos string, probe func() (HWCaps, error)) (VideoEncode, error) {
	x264 := VideoEncode{Encoder: EncoderX264, HWAccel: HWAccelNone}
	switch setting {
	case model.PrepareEncoderX264:
		return x264, nil
	case "", model.PrepareEncoderAuto, model.PrepareEncoderVideoToolbox:
	default:
		return x264, fmt.Errorf("prepare.encoder must be auto, videotoolbox or x264, got %q", setting)
	}
	explicit := setting == model.PrepareEncoderVideoToolbox
	if !explicit && goos != "darwin" {
		return x264, nil
	}
	caps, err := probe()
	if err != nil {
		return x264, err
	}
	if !caps.Encoder {
		if explicit {
			return x264, fmt.Errorf("prepare.encoder is videotoolbox but this ffmpeg cannot encode with %s (see logs/prepare.log); set prepare.encoder to auto or x264", EncoderVideoToolbox)
		}
		return x264, nil
	}
	vt := VideoEncode{Encoder: EncoderVideoToolbox, HWAccel: HWAccelNone}
	if caps.HWAccel {
		vt.HWAccel = HWAccelVideoToolbox
	}
	return vt, nil
}
