package media

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ErrIrregularTiming is returned when an encode prepare produced does not
// run at a constant frame rate. OpusClip's full-video renderer mis-renders
// such a file: on a 24-minute recording with two frames held twice as long
// it rendered half the video once and stalled the next time.
var ErrIrregularTiming = errors.New("irregular frame timing")

// Packet-timing tolerances: a packet's duration may differ from the nominal
// frame duration by up to 10%, and consecutive presentation times may be
// 0.5 to 1.5 frames apart. An iPhone's own timestamps jitter by 1/600 s
// (5% of a 30 fps frame) and pass; a dropped or held frame does not.
const (
	MaxDurationDeviation = 0.10
	MaxPTSGapFrames      = 1.5
	MinPTSGapFrames      = 0.5
)

// FrameDuration is one frame's duration in seconds at rate, an ffprobe
// rational ("30/1", "30000/1001") or a plain number ("30", "29.97"). ok is
// false when rate is empty, "0/0" or not a positive number.
func FrameDuration(rate string) (sec float64, ok bool) {
	fps, ok := rateValue(rate)
	if !ok {
		return 0, false
	}
	return 1 / fps, true
}

// IsWholeRate reports whether rate is a whole number of frames per second
// ("30/1", "30", "60/2"). OpusClip does not support other rates.
func IsWholeRate(rate string) bool {
	fps, ok := rateValue(rate)
	return ok && math.Abs(fps-math.Round(fps)) < 1e-9
}

func rateValue(rate string) (float64, bool) {
	num, den, hasDen := strings.Cut(strings.TrimSpace(rate), "/")
	n, err := strconv.ParseFloat(num, 64)
	if err != nil || n <= 0 || math.IsInf(n, 0) {
		return 0, false
	}
	d := 1.0
	if hasDen {
		if d, err = strconv.ParseFloat(den, 64); err != nil || d <= 0 || math.IsInf(d, 0) {
			return 0, false
		}
	}
	return n / d, true
}

// Packet is one video packet as ffprobe reports it, times in seconds.
type Packet struct {
	PTS         float64
	HasPTS      bool
	Duration    float64
	HasDuration bool
	Discard     bool // flags carry D: the demuxer drops the frame (edit-list pre-roll)
}

// ParsePackets parses `ffprobe -show_entries packet=pts_time,duration_time,flags
// -of compact=p=0`, one packet per line ("pts_time=1.033333|duration_time=0.033333|flags=K__").
// A field that is N/A is left unset.
func ParsePackets(out []byte) ([]Packet, error) {
	var pkts []Packet
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var p Packet
		for _, field := range strings.Split(line, "|") {
			k, v, ok := strings.Cut(field, "=")
			if !ok {
				return nil, fmt.Errorf("ffprobe packets: unexpected field %q in %q", field, line)
			}
			switch k {
			case "pts_time":
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					p.PTS, p.HasPTS = f, true
				} else if v != "N/A" {
					return nil, fmt.Errorf("ffprobe packets: pts_time %q: %w", v, err)
				}
			case "duration_time":
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					p.Duration, p.HasDuration = f, true
				} else if v != "N/A" {
					return nil, fmt.Errorf("ffprobe packets: duration_time %q: %w", v, err)
				}
			case "flags":
				p.Discard = strings.Contains(v, "D")
			}
		}
		pkts = append(pkts, p)
	}
	return pkts, sc.Err()
}

// TimingReport is the result of a packet-timing check, persisted as
// job.extra.prepare.timing_check.
type TimingReport struct {
	File           string    `json:"file,omitempty"`
	Packets        int       `json:"packets"`
	NominalMs      float64   `json:"nominal_ms"`
	MaxDeviationMs float64   `json:"max_deviation_ms"`
	MaxGapMs       float64   `json:"max_gap_ms"`
	BadDurations   int       `json:"bad_durations"`
	BadGaps        int       `json:"bad_gaps"`
	At             []float64 `json:"at_s,omitempty"` // where the first irregularities start, seconds
	OK             bool      `json:"ok"`
	// Enforced is false only for a delivery whose video is the source's own,
	// stream-copied: prepare did not encode it, so an irregularity there is
	// a warning, not a failure.
	Enforced bool `json:"enforced"`
}

const maxReportedAt = 5

// CheckPacketTiming checks that video packets run at a constant rate: every
// packet's duration within MaxDurationDeviation of frameSec, and every gap
// between consecutive presentation times (in presentation order, so B-frame
// reordering is fine) between MinPTSGapFrames and MaxPTSGapFrames frames.
// Packets the demuxer discards are ignored. frameSec <= 0 uses the median
// packet duration. No packets at all is not OK.
func CheckPacketTiming(pkts []Packet, frameSec float64) TimingReport {
	var rep TimingReport
	var durs, pts []float64
	for _, p := range pkts {
		if p.Discard {
			continue
		}
		rep.Packets++
		if p.HasDuration {
			durs = append(durs, p.Duration)
		}
		if p.HasPTS {
			pts = append(pts, p.PTS)
		}
	}
	nominal := frameSec
	if nominal <= 0 && len(durs) > 0 {
		s := append([]float64(nil), durs...)
		sort.Float64s(s)
		nominal = s[len(s)/2]
	}
	if rep.Packets == 0 || nominal <= 0 {
		return rep
	}
	rep.NominalMs = roundMs(nominal)
	at := map[float64]bool{}
	mark := func(t float64) {
		if len(rep.At) < maxReportedAt && !at[t] {
			at[t] = true
			rep.At = append(rep.At, math.Round(t*1000)/1000)
		}
	}
	maxDev := 0.0
	for _, p := range pkts {
		if p.Discard || !p.HasDuration {
			continue
		}
		dev := math.Abs(p.Duration - nominal)
		maxDev = math.Max(maxDev, dev)
		if dev > MaxDurationDeviation*nominal {
			rep.BadDurations++
			if p.HasPTS {
				mark(p.PTS)
			}
		}
	}
	sort.Float64s(pts)
	maxGap := 0.0
	for i := 1; i < len(pts); i++ {
		gap := pts[i] - pts[i-1]
		maxGap = math.Max(maxGap, gap)
		if gap > MaxPTSGapFrames*nominal || gap < MinPTSGapFrames*nominal {
			rep.BadGaps++
			mark(pts[i-1]) // the frame held too long (or the duplicate)
		}
	}
	sort.Float64s(rep.At)
	rep.MaxDeviationMs = roundMs(maxDev)
	rep.MaxGapMs = roundMs(maxGap)
	rep.OK = rep.BadDurations == 0 && rep.BadGaps == 0
	return rep
}

func roundMs(sec float64) float64 { return math.Round(sec*1e6) / 1000 }

// Summary is the one-line form for logs and errors.
func (r TimingReport) Summary() string {
	if r.Packets == 0 {
		return "no video packets"
	}
	if r.NominalMs == 0 {
		return fmt.Sprintf("%d packets and no frame rate or packet durations to check them against", r.Packets)
	}
	s := fmt.Sprintf("%d packets at %.3f ms/frame, max deviation %.3f ms, max gap %.3f ms", r.Packets, r.NominalMs, r.MaxDeviationMs, r.MaxGapMs)
	if r.OK {
		return s + ", constant frame rate"
	}
	at := make([]string, len(r.At))
	for i, t := range r.At {
		at[i] = strconv.FormatFloat(t, 'f', 3, 64) + " s"
	}
	return fmt.Sprintf("%s; %d packet durations and %d gaps off the frame grid, at %s", s, r.BadDurations, r.BadGaps, strings.Join(at, ", "))
}

// Packets lists the first video stream's packet times with ffprobe: one read
// through the video stream without decoding (0.8 s for a 4.7 GB, 24-minute
// file in the page cache).
func (t Tools) Packets(path string) ([]Packet, error) {
	out, err := t.run("", t.FFprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time,duration_time,flags", "-of", "compact=p=0", path)
	if err != nil {
		return nil, err
	}
	return ParsePackets(out)
}

// CheckTiming runs CheckPacketTiming on path's video packets against
// frameSec, the duration of one frame at the source's rate (<= 0 = the
// file's median packet duration).
func (t Tools) CheckTiming(path string, frameSec float64) (TimingReport, error) {
	pkts, err := t.Packets(path)
	if err != nil {
		return TimingReport{}, err
	}
	return CheckPacketTiming(pkts, frameSec), nil
}
