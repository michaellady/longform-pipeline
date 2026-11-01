package media

import (
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestFrameRateHelpers(t *testing.T) {
	for _, c := range []struct {
		rate  string
		ok    bool
		fps   float64
		whole bool
	}{
		{"30/1", true, 30, true},
		{"30", true, 30, true},
		{"60/2", true, 30, true},
		{"25/1", true, 25, true},
		{"30000/1001", true, 30000.0 / 1001, false},
		{"29.97", true, 29.97, false},
		{"13010400/433703", true, 13010400.0 / 433703, false},
		{"", false, 0, false},
		{"0/0", false, 0, false},
		{"30/0", false, 0, false},
		{"-30/1", false, 0, false},
		{"N/A", false, 0, false},
	} {
		sec, ok := FrameDuration(c.rate)
		if ok != c.ok || (ok && math.Abs(sec-1/c.fps) > 1e-12) {
			t.Errorf("FrameDuration(%q) = %v %v, want 1/%v %v", c.rate, sec, ok, c.fps, c.ok)
		}
		if w := IsWholeRate(c.rate); w != c.whole {
			t.Errorf("IsWholeRate(%q) = %v, want %v", c.rate, w, c.whole)
		}
	}
}

// The real 24-minute iPhone recording: r_frame_rate 30/1, but its 1/600 s
// timestamp jitter makes avg_frame_rate 13010400/433703. The rate prepare
// encodes at is r_frame_rate; FPS (persisted) keeps avg_frame_rate.
func TestInfoRate(t *testing.T) {
	vfr := strings.Replace(probeRotated, `"avg_frame_rate": "30/1"`, `"avg_frame_rate": "13010400/433703"`, 1)
	if vfr == probeRotated {
		t.Fatal("fixture lacks avg_frame_rate 30/1")
	}
	info, err := ParseProbe([]byte(vfr))
	if err != nil {
		t.Fatal(err)
	}
	if info.Rate() != "30/1" || info.RFrameRate != "30/1" || info.FPS != "13010400/433703" {
		t.Errorf("Rate %q RFrameRate %q FPS %q", info.Rate(), info.RFrameRate, info.FPS)
	}
	// A staged probe has only the rounded FPS.
	if r := (&Info{FPS: "30"}).Rate(); r != "30" {
		t.Errorf("staged Rate = %q", r)
	}
	if r := (&Info{RFrameRate: "0/0", FPS: "0/0"}).Rate(); r != "" {
		t.Errorf("no-rate Rate = %q", r)
	}
}

func TestParsePackets(t *testing.T) {
	out := "pts_time=-0.300000|duration_time=0.033333|flags=KD_\n" +
		"pts_time=0.000000|duration_time=0.033333|flags=K__\n" +
		"\n" +
		"pts_time=N/A|duration_time=N/A|flags=___\n" +
		"duration_time=0.066667|pts_time=0.066667|flags=___\n"
	got, err := ParsePackets([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []Packet{
		{PTS: -0.3, HasPTS: true, Duration: 0.033333, HasDuration: true, Discard: true},
		{PTS: 0, HasPTS: true, Duration: 0.033333, HasDuration: true},
		{},
		{PTS: 0.066667, HasPTS: true, Duration: 0.066667, HasDuration: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParsePackets:\n got %+v\nwant %+v", got, want)
	}
	for _, bad := range []string{"pts_time=x|duration_time=0.1\n", "garbage\n", "pts_time=0|duration_time=1e\n"} {
		if _, err := ParsePackets([]byte(bad)); err == nil {
			t.Errorf("ParsePackets(%q) accepted", bad)
		}
	}
}

// ffprobe prints times to the microsecond.
func us(sec float64) float64 { return math.Round(sec*1e6) / 1e6 }

// cfrPackets is n frames at fps in B-frame decode order (each pair after a
// reference frame swapped), as an encoder writes them; held lists frames
// shown for two frame times, each pushing later frames back by one.
func cfrPackets(n int, fps float64, held ...int) []Packet {
	isHeld := map[int]bool{}
	for _, h := range held {
		isHeld[h] = true
	}
	pts := make([]Packet, n)
	slot := 0
	for i := 0; i < n; i++ {
		d := 1.0
		if isHeld[i] {
			d = 2
		}
		pts[i] = Packet{PTS: us(float64(slot) / fps), HasPTS: true, Duration: us(d / fps), HasDuration: true}
		slot += int(d)
	}
	for i := 1; i+1 < n; i += 3 {
		pts[i], pts[i+1] = pts[i+1], pts[i]
	}
	return pts
}

func TestCheckPacketTiming(t *testing.T) {
	const frame30 = 1.0 / 30

	t.Run("constant 30 fps in decode order", func(t *testing.T) {
		rep := CheckPacketTiming(cfrPackets(43370, 30), frame30)
		if !rep.OK || rep.Packets != 43370 || rep.NominalMs != 33.333 || rep.MaxDeviationMs > 0.001 || rep.MaxGapMs > 33.334 || len(rep.At) != 0 {
			t.Errorf("%+v", rep)
		}
	})

	// The delivery that broke OpusClip: 43368 packets, two of them held for
	// two frames (at 300.467 s and 932.933 s).
	t.Run("two held frames", func(t *testing.T) {
		rep := CheckPacketTiming(cfrPackets(43368, 30, 9014, 27987), frame30)
		if rep.OK || rep.BadDurations != 2 || rep.BadGaps != 2 {
			t.Fatalf("%+v", rep)
		}
		if !reflect.DeepEqual(rep.At, []float64{300.467, 932.933}) {
			t.Errorf("at %v, want [300.467 932.933]", rep.At)
		}
		if rep.MaxDeviationMs != 33.334 && rep.MaxDeviationMs != 33.333 {
			t.Errorf("max deviation %v ms, want 33.333", rep.MaxDeviationMs)
		}
		if rep.MaxGapMs < 66.66 || rep.MaxGapMs > 66.67 {
			t.Errorf("max gap %v ms, want 66.667", rep.MaxGapMs)
		}
		s := rep.Summary()
		for _, want := range []string{"43368 packets", "2 packet durations and 2 gaps", "300.467 s, 932.933 s"} {
			if !strings.Contains(s, want) {
				t.Errorf("summary %q lacks %q", s, want)
			}
		}
	})

	// An iPhone's own timestamps sit on a 1/600 s grid: frames of 19, 20 or
	// 21 ticks (31.667, 33.333, 35 ms) average 30 fps and must pass.
	t.Run("iPhone 1/600 s jitter", func(t *testing.T) {
		var pkts []Packet
		ticks := 0
		for i := 0; i < 900; i++ {
			d := []int{20, 21, 19, 20}[i%4]
			pkts = append(pkts, Packet{PTS: us(float64(ticks) / 600), HasPTS: true, Duration: us(float64(d) / 600), HasDuration: true})
			ticks += d
		}
		if rep := CheckPacketTiming(pkts, frame30); !rep.OK || rep.MaxDeviationMs != 1.667 {
			t.Errorf("%+v", rep)
		}
	})

	// A missing frame whose neighbours kept a one-frame duration shows only
	// as a presentation gap.
	t.Run("gap without a long duration", func(t *testing.T) {
		pkts := cfrPackets(300, 30)
		for i := range pkts {
			if pkts[i].PTS > 5 {
				pkts[i].PTS = us(pkts[i].PTS + frame30)
			}
		}
		rep := CheckPacketTiming(pkts, frame30)
		if rep.OK || rep.BadDurations != 0 || rep.BadGaps != 1 || len(rep.At) != 1 {
			t.Errorf("%+v", rep)
		}
	})

	t.Run("duplicate timestamp", func(t *testing.T) {
		pkts := cfrPackets(300, 30)
		pkts = append(pkts, pkts[100])
		if rep := CheckPacketTiming(pkts, frame30); rep.OK || rep.BadGaps != 1 {
			t.Errorf("%+v", rep)
		}
	})

	// Edit-list pre-roll (a stream-copy cut): discarded packets with
	// negative times are not frames anyone sees.
	t.Run("discarded pre-roll", func(t *testing.T) {
		pkts := []Packet{
			{PTS: -0.3, HasPTS: true, Duration: 0.1, HasDuration: true, Discard: true},
			{PTS: -0.4, HasPTS: true, Duration: 0.033333, HasDuration: true, Discard: true},
		}
		pkts = append(pkts, cfrPackets(300, 30)...)
		if rep := CheckPacketTiming(pkts, frame30); !rep.OK || rep.Packets != 300 {
			t.Errorf("%+v", rep)
		}
	})

	t.Run("NTSC rate", func(t *testing.T) {
		if rep := CheckPacketTiming(cfrPackets(1800, 30000.0/1001), 1001.0/30000); !rep.OK || rep.NominalMs != 33.367 {
			t.Errorf("%+v", rep)
		}
	})

	// A file encoded at the wrong rate fails against the source's.
	t.Run("wrong rate", func(t *testing.T) {
		rep := CheckPacketTiming(cfrPackets(250, 25), frame30)
		if rep.OK || rep.BadDurations != 250 || len(rep.At) != maxReportedAt {
			t.Errorf("%+v", rep)
		}
	})

	t.Run("median nominal when the rate is unknown", func(t *testing.T) {
		rep := CheckPacketTiming(cfrPackets(250, 25, 100), 0)
		if rep.OK || rep.NominalMs != 40 || rep.BadDurations != 1 {
			t.Errorf("%+v", rep)
		}
		if rep := CheckPacketTiming(cfrPackets(250, 25), 0); !rep.OK {
			t.Errorf("%+v", rep)
		}
	})

	t.Run("nothing to measure against", func(t *testing.T) {
		rep := CheckPacketTiming([]Packet{{PTS: 0, HasPTS: true}, {PTS: 0.04, HasPTS: true}}, 0)
		if rep.OK || rep.Packets != 2 || !strings.Contains(rep.Summary(), "no frame rate or packet durations") {
			t.Errorf("%+v %q", rep, rep.Summary())
		}
	})

	t.Run("no packets", func(t *testing.T) {
		rep := CheckPacketTiming(nil, frame30)
		if rep.OK || rep.Packets != 0 || rep.Summary() != "no video packets" {
			t.Errorf("%+v %q", rep, rep.Summary())
		}
	})
}

// The checker against a real file: LONGFORM_TIMING_FILE names it (read only),
// LONGFORM_TIMING_RATE the source rate (default 30/1) and
// LONGFORM_TIMING_WANT "ok" or "irregular" what it must find.
func TestIntegrationTimingCheckFile(t *testing.T) {
	path := os.Getenv("LONGFORM_TIMING_FILE")
	if os.Getenv("LONGFORM_INTEGRATION") != "1" || path == "" {
		t.Skip("set LONGFORM_INTEGRATION=1 and LONGFORM_TIMING_FILE to check a real file")
	}
	tools, err := FindTools()
	if err != nil {
		t.Fatal(err)
	}
	rate := os.Getenv("LONGFORM_TIMING_RATE")
	if rate == "" {
		rate = "30/1"
	}
	frame, _ := FrameDuration(rate)
	rep, err := tools.CheckTiming(path, frame)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s at %s: %s", path, rate, rep.Summary())
	t.Logf("report %+v", rep)
	switch os.Getenv("LONGFORM_TIMING_WANT") {
	case "ok":
		if !rep.OK {
			t.Error("want constant frame rate")
		}
	case "irregular":
		if rep.OK {
			t.Error("want irregular timing flagged")
		}
	}
}
