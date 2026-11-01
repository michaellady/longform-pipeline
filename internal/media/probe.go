package media

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// Info is everything prepare branches on, parsed from ffprobe JSON. It is a
// superset of model.Probe (which is what gets persisted in job.json).
type Info struct {
	DurationMs     int64
	Width          int
	Height         int
	FPS            string // avg_frame_rate, else r_frame_rate (persisted as model.Probe.FPS)
	RFrameRate     string // r_frame_rate exactly as ffprobe gives it ("30/1"); "" on a staged probe
	VideoCodec     string
	AudioCodec     string
	PixFmt         string
	ColorTransfer  string
	ColorPrimaries string
	ColorSpace     string
	Rotation       int // 0, 90, 180, 270 (display-matrix rotation normalised to [0,360))
	HasAudio       bool
	SampleRate     int
	Channels       int
	ChannelLayout  string
}

// IsHDRTransfer reports whether a color_transfer tag means HDR: PQ
// (smpte2084) or HLG (arib-std-b67), the two iPhone HDR encodings.
func IsHDRTransfer(transfer string) bool {
	switch strings.ToLower(strings.TrimSpace(transfer)) {
	case "smpte2084", "arib-std-b67":
		return true
	}
	return false
}

// HDR reports whether the video stream needs tonemapping.
func (i *Info) HDR() bool { return IsHDRTransfer(i.ColorTransfer) }

// Rate is the stream's nominal frame rate as an exact ffprobe rational:
// r_frame_rate, else FPS, "" when neither is a positive rate. prepare
// encodes at it and checks packet timing against it. avg_frame_rate is no
// use for this: an iPhone recording whose timestamps jitter by 1/600 s
// averages 13010400/433703.
func (i *Info) Rate() string {
	for _, r := range []string{i.RFrameRate, i.FPS} {
		if _, ok := FrameDuration(r); ok {
			return r
		}
	}
	return ""
}

// Model converts to the persisted probe shape.
func (i *Info) Model() *model.Probe {
	return &model.Probe{
		DurationMs:    i.DurationMs,
		Width:         i.Width,
		Height:        i.Height,
		FPS:           i.FPS,
		Codec:         i.VideoCodec,
		AudioCodec:    i.AudioCodec,
		ColorTransfer: i.ColorTransfer,
		Rotation:      i.Rotation,
		HDR:           i.HDR(),
	}
}

// AudioFormat is the subset the censor filtergraph must match exactly so the
// beep branches mix at unity gain (a mono->stereo upmix would cost -3 dB).
type AudioFormat struct {
	SampleRate int
	Channels   int
	Layout     string // ffmpeg layout name ("stereo", "mono"); empty -> "<N>c"
}

// Audio returns the audio format, defaulting to 48 kHz stereo when the probe
// lacks the fields (never expected for a real recording).
func (i *Info) Audio() AudioFormat {
	af := AudioFormat{SampleRate: i.SampleRate, Channels: i.Channels, Layout: i.ChannelLayout}
	if af.SampleRate == 0 {
		af.SampleRate = 48000
	}
	if af.Channels == 0 {
		af.Channels = 2
	}
	return af
}

// LayoutToken is the channel-layout string used in aformat/pan.
func (af AudioFormat) LayoutToken() string {
	if af.Layout != "" && !strings.ContainsAny(af.Layout, " ()+") {
		return af.Layout
	}
	return fmt.Sprintf("%dc", af.Channels)
}

type ffprobeOut struct {
	Streams []struct {
		CodecType      string            `json:"codec_type"`
		CodecName      string            `json:"codec_name"`
		Width          int               `json:"width"`
		Height         int               `json:"height"`
		PixFmt         string            `json:"pix_fmt"`
		RFrameRate     string            `json:"r_frame_rate"`
		AvgFrameRate   string            `json:"avg_frame_rate"`
		ColorTransfer  string            `json:"color_transfer"`
		ColorPrimaries string            `json:"color_primaries"`
		ColorSpace     string            `json:"color_space"`
		SampleRate     string            `json:"sample_rate"`
		Channels       int               `json:"channels"`
		ChannelLayout  string            `json:"channel_layout"`
		Duration       string            `json:"duration"`
		Tags           map[string]string `json:"tags"`
		SideDataList   []struct {
			SideDataType string  `json:"side_data_type"`
			Rotation     float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// ParseProbe parses `ffprobe -print_format json -show_format -show_streams`.
// The first video and first audio stream win.
func ParseProbe(b []byte) (*Info, error) {
	var out ffprobeOut
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("ffprobe json: %w", err)
	}
	info := &Info{}
	info.DurationMs = secondsToMs(out.Format.Duration)
	var sawVideo, sawAudio bool
	for _, s := range out.Streams {
		switch s.CodecType {
		case "video":
			if sawVideo {
				continue
			}
			sawVideo = true
			info.VideoCodec = s.CodecName
			info.Width, info.Height = s.Width, s.Height
			info.PixFmt = s.PixFmt
			info.ColorTransfer = s.ColorTransfer
			info.ColorPrimaries = s.ColorPrimaries
			info.ColorSpace = s.ColorSpace
			info.FPS = s.AvgFrameRate
			if info.FPS == "" || info.FPS == "0/0" {
				info.FPS = s.RFrameRate
			}
			info.RFrameRate = s.RFrameRate
			for _, sd := range s.SideDataList {
				if sd.SideDataType == "Display Matrix" {
					info.Rotation = normaliseRotation(sd.Rotation)
				}
			}
			if info.Rotation == 0 {
				if r, ok := s.Tags["rotate"]; ok { // pre-ffmpeg-5 files carry a rotate tag
					if f, err := strconv.ParseFloat(r, 64); err == nil {
						info.Rotation = normaliseRotation(f)
					}
				}
			}
			if info.DurationMs == 0 {
				info.DurationMs = secondsToMs(s.Duration)
			}
		case "audio":
			if sawAudio {
				continue
			}
			sawAudio = true
			info.HasAudio = true
			info.AudioCodec = s.CodecName
			info.SampleRate, _ = strconv.Atoi(s.SampleRate)
			info.Channels = s.Channels
			info.ChannelLayout = s.ChannelLayout
		}
	}
	if !sawVideo {
		return nil, fmt.Errorf("ffprobe: no video stream")
	}
	return info, nil
}

func normaliseRotation(r float64) int {
	n := int(math.Round(r)) % 360
	if n < 0 {
		n += 360
	}
	return n
}

func secondsToMs(s string) int64 {
	if s == "" || s == "N/A" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(f * 1000))
}

// Probe runs ffprobe on path.
func (t Tools) Probe(path string) (*Info, error) {
	out, err := t.run("", t.FFprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return nil, err
	}
	return ParseProbe(out)
}
