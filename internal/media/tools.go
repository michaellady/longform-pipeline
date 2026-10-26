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
	HWAccel     bool // `ffmpeg -hwaccels` lists videotoolbox (hardware decode)
	Encoder     bool // h264_videotoolbox is listed and passed a test encode
	ScaleVT     bool // `ffmpeg -filters` lists scale_vt (HDR -> BT.709 on hardware frames)
	TransposeVT bool // `ffmpeg -filters` lists transpose_vt (rotation on hardware frames)
}

// VideoToolboxCaps probes the real ffmpeg: hardware decode, the encoder and
// the two VideoToolbox filters the HDR conversion uses. Being listed is not
// enough for the encoder: it must also encode two test frames with the exact options prepare
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
	out, err = t.run("", t.FFmpeg, "-hide_banner", "-filters")
	if err != nil {
		return c, err
	}
	c.ScaleVT = ParseFilterList(out, "scale_vt")
	c.TransposeVT = ParseFilterList(out, "transpose_vt")
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

// Values of extra.prepare.tonemap_filter: how prepare converted an HDR source
// to BT.709 SDR, or why it could not.
const (
	TonemapScaleVT     = "scale_vt"    // VideoToolbox decode, scale_vt (+ transpose_vt), h264_videotoolbox
	TonemapZscale      = "zscale"      // software: zscale linearise, tonemap, zscale to BT.709
	TonemapUnavailable = "unavailable" // neither: prepare failed instead of encoding
)

// ErrNoHDRConversion is returned for an HDR source when this ffmpeg has no
// correct conversion to SDR. prepare fails rather than fall back to the plain
// tonemap filter, whose output on unlinearised HLG is far too dark.
var ErrNoHDRConversion = errors.New("this ffmpeg has no correct HDR to SDR conversion")

// TonemapPlan is how prepare's HDR pass converts to BT.709 SDR.
type TonemapPlan struct {
	Filter string      // TonemapScaleVT | TonemapZscale | TonemapUnavailable
	Encode VideoEncode // how that pass decodes and encodes
}

// ChooseTonemap picks the HDR -> BT.709 conversion. VideoToolbox comes first:
// on darwin, with hardware decode, an h264_videotoolbox encoder that passed
// its test encode, scale_vt, and transpose_vt when the source is rotated,
// the frames stay on the media engine from decoder to encoder. scale_vt only
// takes hardware frames and they can only go to that encoder, so this holds
// whatever prepare.encoder says (x264 then applies to the stabilise pass
// only). Otherwise zscale runs in software with enc, the profile's encoder.
// Otherwise the plan is TonemapUnavailable and the error wraps
// ErrNoHDRConversion, naming what is missing and the two ways to a correct
// conversion. caps is called only on darwin and hasZscale only when
// VideoToolbox cannot do it; goos is runtime.GOOS.
func ChooseTonemap(goos string, enc VideoEncode, rotation int, caps func() (HWCaps, error), hasZscale func() (bool, error)) (TonemapPlan, error) {
	var missing []string
	if goos == "darwin" {
		c, err := caps()
		if err != nil {
			return TonemapPlan{}, err
		}
		dir, rightAngle := VTTranspose(rotation)
		switch {
		case !c.HWAccel:
			missing = append(missing, "no -hwaccel videotoolbox")
		case !c.Encoder:
			missing = append(missing, EncoderVideoToolbox+" missing or failed its test encode")
		case !c.ScaleVT:
			missing = append(missing, "no scale_vt filter")
		case !rightAngle:
			missing = append(missing, fmt.Sprintf("rotation %d is not a multiple of 90", rotation))
		case dir != "" && !c.TransposeVT:
			missing = append(missing, "no transpose_vt filter for the rotated source")
		default:
			return TonemapPlan{Filter: TonemapScaleVT, Encode: VideoEncode{
				Encoder: EncoderVideoToolbox, HWAccel: HWAccelVideoToolbox, HWFrames: true, FilterThreads: enc.FilterThreads,
			}}, nil
		}
	} else {
		missing = append(missing, "VideoToolbox needs macOS")
	}
	has, err := hasZscale()
	if err != nil {
		return TonemapPlan{}, err
	}
	if has {
		return TonemapPlan{Filter: TonemapZscale, Encode: enc}, nil
	}
	missing = append(missing, "no zscale filter")
	return TonemapPlan{Filter: TonemapUnavailable}, fmt.Errorf("%w (%s). Either run on a Mac whose ffmpeg has VideoToolbox "+
		"(-hwaccel videotoolbox, a working %s and the scale_vt filter, as Homebrew's ffmpeg on Apple Silicon does) "+
		"or install an ffmpeg built with zimg (the zscale filter). Nothing was encoded: the tonemap filter without "+
		"zscale makes a dark picture", ErrNoHDRConversion, strings.Join(missing, "; "), EncoderVideoToolbox)
}
