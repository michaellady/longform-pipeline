package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// ProbeResult is model.Probe plus the fields staging itself needs.
type ProbeResult struct {
	Probe        model.Probe
	CreationTime time.Time // zero when the container carries no creation_time
	HasVideo     bool
	HasAudio     bool
}

type ffprobeJSON struct {
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeStream struct {
	CodecType     string            `json:"codec_type"`
	CodecName     string            `json:"codec_name"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	RFrameRate    string            `json:"r_frame_rate"`
	AvgFrameRate  string            `json:"avg_frame_rate"`
	ColorTransfer string            `json:"color_transfer"`
	Tags          map[string]string `json:"tags"`
	SideDataList  []map[string]any  `json:"side_data_list"`
}

// Probe runs ffprobe on one file and extracts the fields the pipeline
// branches on. It returns ErrNoFFprobe when the tool is absent.
func Probe(path string) (*ProbeResult, error) {
	ffprobe := LookPath("ffprobe")
	if ffprobe == "" {
		return nil, ErrNoFFprobe
	}
	cmd := exec.Command(ffprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %v: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return parseProbe(out)
}

func parseProbe(out []byte) (*ProbeResult, error) {
	var raw ffprobeJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ffprobe output: %w", err)
	}
	r := &ProbeResult{}
	if d, err := strconv.ParseFloat(raw.Format.Duration, 64); err == nil {
		r.Probe.DurationMs = int64(math.Round(d * 1000))
	}
	r.CreationTime = parseCreationTime(raw.Format.Tags["creation_time"])
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if r.HasVideo {
				continue
			}
			r.HasVideo = true
			r.Probe.Width, r.Probe.Height = s.Width, s.Height
			r.Probe.FPS = formatRate(s.RFrameRate, s.AvgFrameRate)
			r.Probe.Codec = s.CodecName
			r.Probe.ColorTransfer = s.ColorTransfer
			r.Probe.Rotation = rotationOf(s)
			r.Probe.HDR = s.ColorTransfer == "smpte2084" || s.ColorTransfer == "arib-std-b67"
			if r.CreationTime.IsZero() {
				r.CreationTime = parseCreationTime(s.Tags["creation_time"])
			}
		case "audio":
			if r.HasAudio {
				continue
			}
			r.HasAudio = true
			r.Probe.AudioCodec = s.CodecName
		}
	}
	if !r.HasVideo {
		return nil, errors.New("no video stream")
	}
	return r, nil
}

// formatRate turns an ffprobe rational ("30000/1001") into a decimal string
// with at most three decimals ("29.97"). The first parseable rate wins.
func formatRate(rates ...string) string {
	for _, r := range rates {
		num, den, hasDen := strings.Cut(r, "/")
		n, err := strconv.ParseFloat(num, 64)
		if err != nil || n == 0 {
			continue
		}
		d := 1.0
		if hasDen {
			if d, err = strconv.ParseFloat(den, 64); err != nil || d == 0 {
				continue
			}
		}
		return strconv.FormatFloat(math.Round(n/d*1000)/1000, 'f', -1, 64)
	}
	return ""
}

func rotationOf(s ffprobeStream) int {
	for _, sd := range s.SideDataList {
		if v, ok := sd["rotation"]; ok {
			if f, ok := v.(float64); ok {
				return int(math.Round(f))
			}
		}
	}
	if v, ok := s.Tags["rotate"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

var creationLayouts = []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000000Z", "2006-01-02 15:04:05"}

func parseCreationTime(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	for _, l := range creationLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t
		}
	}
	return time.Time{}
}
