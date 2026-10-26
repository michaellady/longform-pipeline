// Package prepare takes a job from `staged` to `prepared`: remux or tonemap,
// optional vidstab, whisper transcript, censor spans, delivery.mp4 (plan
// section 4.3). Every sub-step is skipped on re-run when its output exists
// and its input is unchanged (size + mtime + a step signature, recorded in
// job.extra.prepare), so a crash or a profile tweak resumes from the step
// that changed.
package prepare

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/michaellady/longform-pipeline/internal/censor"
	"github.com/michaellady/longform-pipeline/internal/ingest"
	"github.com/michaellady/longform-pipeline/internal/media"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/transcribe"
)

// Job-relative paths the stage writes.
const (
	RemuxOut        = "analysis/remux.mp4"
	StabilizedOut   = "analysis/stabilized.mp4"
	TransformsOut   = "analysis/transforms.trf"
	StabilizeBefore = "review/stabilize-before.jpg"
	StabilizeAfter  = "review/stabilize-after.jpg"
	WavOut          = "analysis/audio16k.wav"
	WhisperBase     = "analysis/whisper" // whisper-cli appends .json
	WhisperOut      = "analysis/whisper.json"
	TranscriptOut   = "analysis/transcript.json"
	CensoredOut     = "analysis/censored.json"
	DeliveryOut     = "delivery.mp4"
	LogOut          = "logs/prepare.log"
)

// Options wires the external tools. Zero values are resolved from PATH.
type Options struct {
	Tools      media.Tools
	WhisperCLI string
	CensorList string    // path; empty = censor.FindDefaultList()
	Log        io.Writer // step progress lines (stdout in the CLI)
}

// State is persisted under job.Extra["prepare"].
type State struct {
	Steps             map[string]Stamp `json:"steps"`
	HDR               bool             `json:"hdr"`
	TonemapFilter     string           `json:"tonemap_filter,omitempty"` // "scale_vt" | "zscale" | "unavailable" (media.Tonemap*)
	Stabilized        bool             `json:"stabilized"`
	StabilizeMethod   string           `json:"stabilize_method,omitempty"` // "vidstab" | "deshake ..." | "skipped: ..."
	TranscribeSkipped bool             `json:"transcribe_skipped,omitempty"`
	WhisperModel      string           `json:"whisper_model,omitempty"`
	CensorList        string           `json:"censor_list,omitempty"`
	CensorSpans       int              `json:"censor_spans"`
	// Encoder and HWAccel record the profile's prepare.encoder as resolved
	// on this machine, which the stabilise pass and a zscale tonemap use:
	// "h264_videotoolbox" | "libx264", and "videotoolbox" | "none". Empty
	// when nothing was re-encoded. A scale_vt tonemap always decodes and
	// encodes on VideoToolbox, whatever these say.
	Encoder string `json:"encoder,omitempty"`
	HWAccel string `json:"hwaccel,omitempty"`
	// FrameRate is the source's rate (ffprobe's r_frame_rate) that every
	// encode is pinned to at a constant frame rate.
	FrameRate string `json:"frame_rate,omitempty"`
	// TimingCheck is the last packet-timing check: the delivery's when
	// prepare finished, else the file that failed it.
	TimingCheck *media.TimingReport `json:"timing_check,omitempty"`
}

// Stamp records what a step consumed and produced.
type Stamp struct {
	Input  string `json:"input"`
	Size   int64  `json:"size"`
	Mtime  int64  `json:"mtime_unix"`
	Sig    string `json:"sig,omitempty"`
	Output string `json:"output"`
	At     string `json:"at"`
}

// ErrState is returned when the job is not in `staged`.
var ErrState = errors.New("job is not staged")

type runner struct {
	job   *model.Job
	prof  *model.Profile
	opts  Options
	state *State
	log   io.Writer
	enc   *media.VideoEncode // resolved on first re-encode
	hw    *media.HWCaps      // probed at most once per run
}

// Run executes the stage. It is idempotent: a job already at or past
// `prepared` returns nil immediately.
func Run(job *model.Job, prof *model.Profile, opts Options) error {
	if job.AtLeast(model.StatePrepared) {
		return nil
	}
	if job.State != model.StateStaged {
		return fmt.Errorf("%w: state is %s", ErrState, job.State)
	}
	if len(job.Sources) == 0 {
		return errors.New("prepare: job has no sources")
	}
	if opts.Tools.FFmpeg == "" || opts.Tools.FFprobe == "" {
		t, err := media.FindTools()
		if err != nil {
			return err
		}
		t.Log = opts.Tools.Log
		opts.Tools = t
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if err := os.MkdirAll(job.Path("logs"), 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(job.Path(LogOut), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	fmt.Fprintf(lf, "==== prepare %s %s\n", job.ID, time.Now().UTC().Format(time.RFC3339))
	if opts.Tools.Log == nil {
		opts.Tools.Log = lf
	} else {
		opts.Tools.Log = io.MultiWriter(opts.Tools.Log, lf)
	}
	r := &runner{job: job, prof: prof, opts: opts, state: loadState(job), log: lf}
	if err := r.run(); err != nil {
		fmt.Fprintf(lf, "ERROR: %v\n", err)
		job.Error = err.Error()
		_ = r.saveState()
		return err
	}
	return nil
}

func (r *runner) run() error {
	job, prof := r.job, r.prof
	// The input is the stitched segment for a stitch profile, else the single
	// staged source; ingest.InputPath is the one place that rule lives.
	src := ingest.InputPath(job)
	if src == "" {
		return fmt.Errorf("prepare: job has no input (no sources, no stitched file)")
	}
	if !filepath.IsAbs(src) {
		src = job.Path(src)
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("prepare: source: %w", err)
	}

	// 1. Probe the source (reuse the staged probe for the HDR/rotation call).
	// A stitched job's Sources[0] probe describes only the first segment, so
	// the stitched file is always probed afresh.
	var info *media.Info
	if p := job.Sources[0].Probe; p != nil && !job.Stitched {
		info = &media.Info{DurationMs: p.DurationMs, Width: p.Width, Height: p.Height, FPS: p.FPS, VideoCodec: p.Codec, AudioCodec: p.AudioCodec, ColorTransfer: p.ColorTransfer, Rotation: p.Rotation, HasAudio: p.AudioCodec != ""}
	} else {
		var err error
		if info, err = r.opts.Tools.Probe(src); err != nil {
			return err
		}
		if !job.Stitched {
			job.Sources[0].Probe = info.Model()
			if err := job.Save(); err != nil {
				return err
			}
		}
	}
	r.state.HDR = info.HDR()
	r.state.Encoder, r.state.HWAccel = "", ""
	r.state.TimingCheck = nil
	rate, err := r.sourceRate(src, info)
	if err != nil {
		return err
	}
	r.state.FrameRate = rate
	frameSec, _ := media.FrameDuration(rate)

	// 2. Remux, or tonemap when HDR. The conversion is chosen afresh every
	// run (never reused from an earlier run's state), and an HDR source with
	// no correct conversion stops the stage here, before anything is encoded:
	// tonemap_filter "unavailable" is saved with the error.
	remuxSig := "copy"
	var enc media.VideoEncode
	var tm media.TonemapPlan
	r.state.TonemapFilter = ""
	if info.HDR() {
		var err error
		if enc, err = r.encoder(); err != nil {
			return err
		}
		hasZscale := func() (bool, error) { return r.opts.Tools.HasFilter("zscale") }
		tm, err = media.ChooseTonemap(runtime.GOOS, enc, info.Rotation, r.caps, hasZscale)
		r.state.TonemapFilter = tm.Filter
		if err != nil {
			if errors.Is(err, media.ErrNoHDRConversion) {
				fmt.Fprintf(r.opts.Log, "%-11s %s\n", "tonemap", media.TonemapUnavailable)
				return fmt.Errorf("%s is HDR (%s), and %w", filepath.Base(src), info.ColorTransfer, err)
			}
			return err
		}
		tm.Encode.Rate = rate
		remuxSig = "tonemap:" + tm.Filter + encoderSig(tm.Encode) + cfrSig(rate)
		how := "software, " + tm.Encode.EncoderName()
		if tm.Filter == media.TonemapScaleVT {
			how = "VideoToolbox decode and encode"
			if dir, _ := media.VTTranspose(info.Rotation); dir != "" {
				how += ", transpose " + dir
			}
		}
		fmt.Fprintf(r.opts.Log, "%-11s %s (%s)\n", "tonemap", tm.Filter, how)
	}
	err = r.step("remux", src, RemuxOut, remuxSig, func() error {
		if !info.HDR() {
			return r.opts.Tools.Remux(job.Dir(), src, RemuxOut)
		}
		vf := media.ScaleVTTonemapFilter(info.Rotation)
		if tm.Filter == media.TonemapZscale {
			// A reused staged probe lacks primaries/matrix; probe now for them.
			full, perr := r.opts.Tools.Probe(src)
			if perr != nil {
				return perr
			}
			vf = media.ZscaleTonemapFilter(full.ColorTransfer, full.ColorPrimaries, full.ColorSpace)
		}
		if err := r.opts.Tools.Tonemap(job.Dir(), src, RemuxOut, vf, tm.Encode); err != nil {
			return err
		}
		return r.checkTiming(RemuxOut, frameSec, true)
	})
	if err != nil {
		return err
	}
	current := RemuxOut

	// 3. Stabilise.
	r.state.Stabilized = false
	r.state.StabilizeMethod = ""
	if prof.Stabilize.Enabled {
		s := prof.Stabilize
		// The local ffmpeg decides the method: two-pass vidstab when it is
		// built in, the single-pass deshake filter otherwise, and no
		// stabilisation at all when neither exists. A missing filter is a tool
		// change on this machine (Homebrew's ffmpeg 9.0.2 dropped vidstab), not
		// a reason to fail the job.
		hasVidstab, _ := r.opts.Tools.HasFilter("vidstabdetect")
		hasDeshake, _ := r.opts.Tools.HasFilter("deshake")
		if hasVidstab || hasDeshake {
			var err error
			if enc, err = r.encoder(); err != nil {
				return err
			}
			enc.Rate = rate
		}
		switch {
		case hasVidstab:
			sig := fmt.Sprintf("vidstab:%d:%d:%d", s.Shakiness, s.Smoothing, s.Zoom) + encoderSig(enc) + cfrSig(rate)
			err := r.step("stabilize", job.Path(RemuxOut), StabilizedOut, sig, func() error {
				if err := r.opts.Tools.StabilizeDetect(job.Dir(), RemuxOut, media.StabilizeDetectFilter(s.Shakiness, TransformsOut)); err != nil {
					return err
				}
				if err := r.opts.Tools.StabilizeTransform(job.Dir(), RemuxOut, StabilizedOut, media.StabilizeTransformFilter(s.Smoothing, s.Zoom, TransformsOut), enc); err != nil {
					return err
				}
				if err := r.checkTiming(StabilizedOut, frameSec, true); err != nil {
					return err
				}
				at := info.DurationMs / 4
				if err := r.opts.Tools.ExtractFrame(job.Dir(), RemuxOut, at, StabilizeBefore); err != nil {
					return err
				}
				return r.opts.Tools.ExtractFrame(job.Dir(), StabilizedOut, at, StabilizeAfter)
			})
			if err != nil {
				return err
			}
			current = StabilizedOut
			r.state.Stabilized = true
			job.Artifacts.StabilizeBefore = StabilizeBefore
			job.Artifacts.StabilizeAfter = StabilizeAfter
			r.state.StabilizeMethod = "vidstab"
		case hasDeshake:
			sig := "deshake" + encoderSig(enc) + cfrSig(rate)
			err := r.step("stabilize", job.Path(RemuxOut), StabilizedOut, sig, func() error {
				if err := r.opts.Tools.StabilizeTransform(job.Dir(), RemuxOut, StabilizedOut, "deshake", enc); err != nil {
					return err
				}
				if err := r.checkTiming(StabilizedOut, frameSec, true); err != nil {
					return err
				}
				at := info.DurationMs / 4
				if err := r.opts.Tools.ExtractFrame(job.Dir(), RemuxOut, at, StabilizeBefore); err != nil {
					return err
				}
				return r.opts.Tools.ExtractFrame(job.Dir(), StabilizedOut, at, StabilizeAfter)
			})
			if err != nil {
				return err
			}
			current = StabilizedOut
			r.state.Stabilized = true
			job.Artifacts.StabilizeBefore = StabilizeBefore
			job.Artifacts.StabilizeAfter = StabilizeAfter
			r.state.StabilizeMethod = "deshake (vidstab filter not in this ffmpeg)"
		default:
			fmt.Fprintln(os.Stderr, "stabilize   skip (this ffmpeg has neither vidstab nor deshake)")
			r.state.StabilizeMethod = "skipped: no vidstab or deshake filter in ffmpeg"
		}
	}

	// Probe the intermediate: the censor graph must match its audio format.
	cur, err := r.opts.Tools.Probe(job.Path(current))
	if err != nil {
		return err
	}

	// 4. Transcribe.
	lang := prof.Transcribe.Language
	if lang == "" {
		lang = "en"
	}
	if os.Getenv("LONGFORM_SKIP_TRANSCRIBE") == "1" {
		r.state.TranscribeSkipped = true
		err = r.step("transcript", job.Path(current), TranscriptOut, "skipped", func() error {
			return transcribe.Write(job.Path(TranscriptOut), transcribe.Empty(lang, cur.DurationMs))
		})
		if err != nil {
			return err
		}
	} else if !cur.HasAudio {
		err = r.step("transcript", job.Path(current), TranscriptOut, "no-audio", func() error {
			return transcribe.Write(job.Path(TranscriptOut), transcribe.Empty(lang, cur.DurationMs))
		})
		if err != nil {
			return err
		}
	} else {
		r.state.TranscribeSkipped = false
		modelPath := transcribe.ModelPath(prof.Transcribe.Model)
		r.state.WhisperModel = modelPath
		if err := r.step("wav", job.Path(current), WavOut, "16k-mono", func() error {
			return r.opts.Tools.ExtractWav16k(job.Dir(), current, WavOut)
		}); err != nil {
			return err
		}
		wsig := fmt.Sprintf("whisper:%s:%s:%d", modelPath, lang, prof.Transcribe.Threads)
		if err := r.step("whisper", job.Path(WavOut), WhisperOut, wsig, func() error {
			cli := r.opts.WhisperCLI
			if cli == "" {
				var ferr error
				if cli, ferr = transcribe.FindCLI(); ferr != nil {
					return ferr
				}
			}
			return transcribe.Run(transcribe.Options{CLI: cli, Model: modelPath, Language: lang, Threads: prof.Transcribe.Threads, Log: r.log}, job.Dir(), WavOut, WhisperBase)
		}); err != nil {
			return err
		}
		if err := r.step("transcript", job.Path(WhisperOut), TranscriptOut, "v1", func() error {
			raw, rerr := os.ReadFile(job.Path(WhisperOut))
			if rerr != nil {
				return rerr
			}
			t, nerr := transcribe.Normalize(raw, modelPath, lang, cur.DurationMs)
			if nerr != nil {
				return nerr
			}
			return transcribe.Write(job.Path(TranscriptOut), t)
		}); err != nil {
			return err
		}
	}
	job.Artifacts.Transcript = TranscriptOut

	// 5. Censor spans (always written, possibly empty).
	tr, err := transcribe.Load(job.Path(TranscriptOut))
	if err != nil {
		return err
	}
	mode := prof.Censor.Mode
	if mode == "" {
		mode = censor.ModeBeep
	}
	cen := &censor.Censored{SchemaVersion: "1", Mode: mode, Spans: []censor.Span{}}
	if prof.Censor.Enabled && len(tr.Words) > 0 {
		listPath := r.opts.CensorList
		if listPath == "" {
			if listPath, err = censor.FindDefaultList(); err != nil {
				return err
			}
		}
		list, err := censor.LoadList(listPath)
		if err != nil {
			return err
		}
		r.state.CensorList = listPath
		vocab := censor.Vocabulary(list.Words, prof.Censor.Extra)
		cen.Spans = censor.Merge(censor.Match(tr.Words, vocab, censor.DefaultPadMs, cur.DurationMs))
	}
	if err := censor.Write(job.Path(CensoredOut), cen); err != nil {
		return err
	}
	r.state.CensorSpans = len(cen.Spans)
	job.Artifacts.Censored = CensoredOut

	// 6. delivery.mp4.
	graph := ""
	if cur.HasAudio {
		graph = censor.FilterGraph(cen.Spans, mode, cur.Audio())
	}
	dsig := "copy"
	if graph != "" {
		dsig = "censor:" + censor.Signature(cen)
	}
	err = r.step("delivery", job.Path(current), DeliveryOut, dsig, func() error {
		if graph == "" {
			return r.opts.Tools.Remux(job.Dir(), current, DeliveryOut)
		}
		return r.opts.Tools.RenderAudioGraph(job.Dir(), current, DeliveryOut, graph)
	})
	if err != nil {
		return err
	}
	// The delivery is what OpusClip renders, checked on every run (skipped
	// step or not). Its video is prepare's own encode when the source was
	// HDR or stabilised, and must then be constant frame rate; otherwise it
	// is the source's video stream-copied, whose timing prepare did not make
	// (a screen recording is variable rate by design), so an irregularity is
	// recorded and warned about but does not stop the job.
	if err := r.checkTiming(DeliveryOut, frameSec, info.HDR() || r.state.Stabilized); err != nil {
		return err
	}
	del, err := r.opts.Tools.Probe(job.Path(DeliveryOut))
	if err != nil {
		return err
	}
	job.Delivery = del.Model()
	job.Artifacts.Delivery = DeliveryOut
	if err := r.saveState(); err != nil {
		return err
	}
	note := fmt.Sprintf("hdr=%v stabilized=%v words=%d censored_spans=%d mode=%s", r.state.HDR, r.state.Stabilized, len(tr.Words), len(cen.Spans), mode)
	if r.state.TonemapFilter != "" {
		note += " tonemap=" + r.state.TonemapFilter
	}
	if r.state.Encoder != "" {
		note += fmt.Sprintf(" encoder=%s hwaccel=%s", r.state.Encoder, r.state.HWAccel)
	}
	fmt.Fprintln(r.opts.Log, "prepared:", note)
	return job.Advance(model.StatePrepared, "prepare", note)
}

// encoder resolves the profile's prepare.encoder on the first re-encode of
// the run (probing the local ffmpeg only when the setting needs it) and
// records the choice in the state.
func (r *runner) encoder() (media.VideoEncode, error) {
	if r.enc != nil {
		return *r.enc, nil
	}
	enc, err := media.ResolveEncoder(r.prof.Prepare.Encoder, runtime.GOOS, r.caps)
	if err != nil {
		return enc, fmt.Errorf("prepare: %w", err)
	}
	r.enc = &enc
	r.state.Encoder, r.state.HWAccel = enc.EncoderName(), enc.HWAccelName()
	fmt.Fprintf(r.opts.Log, "%-11s %s, decode hwaccel %s\n", "encoder", r.state.Encoder, r.state.HWAccel)
	return enc, nil
}

// sourceRate is the source's exact frame rate for -r and the timing check:
// r_frame_rate from ffprobe. A staged probe keeps only a rounded copy
// ("29.97" for 30000/1001), so the source is probed again for it. A rate
// that is missing or not a whole number is warned about: OpusClip does not
// support fractional rates, and prepare keeps the source's rate either way.
func (r *runner) sourceRate(src string, info *media.Info) (string, error) {
	rate := info.Rate()
	if info.RFrameRate == "" {
		full, err := r.opts.Tools.Probe(src)
		if err != nil {
			return "", err
		}
		if fr := full.Rate(); fr != "" {
			rate = fr
		}
	}
	switch {
	case rate == "":
		r.warn("the source has no frame rate in its probe; encodes are constant frame rate at ffmpeg's guess and the timing check uses the median packet duration")
	case !media.IsWholeRate(rate):
		r.warn(fmt.Sprintf("source frame rate %s is not a whole number of frames per second; OpusClip does not support fractional frame rates", rate))
	}
	return rate, nil
}

// checkTiming runs the packet-timing check on a job-relative file and records
// it as extra.prepare.timing_check. An irregular file fails with
// media.ErrIrregularTiming when enforce is set and is a warning otherwise.
func (r *runner) checkTiming(rel string, frameSec float64, enforce bool) error {
	rep, err := r.opts.Tools.CheckTiming(r.job.Path(rel), frameSec)
	if err != nil {
		return fmt.Errorf("timing check %s: %w", rel, err)
	}
	rep.File, rep.Enforced = rel, enforce
	r.state.TimingCheck = &rep
	fmt.Fprintf(r.log, "timing %s: %s\n", rel, rep.Summary())
	if rep.OK {
		fmt.Fprintf(r.opts.Log, "%-11s ok   %s (%d packets)\n", "timing", rel, rep.Packets)
		return nil
	}
	if !enforce {
		r.warn(fmt.Sprintf("%s is the source's video, stream-copied, and its frame timing is irregular (%s); prepare did not encode it and uploads it as recorded", rel, rep.Summary()))
		return nil
	}
	return fmt.Errorf("%s: %w: %s. Nothing is uploaded; OpusClip mis-renders a video that is not constant frame rate", rel, media.ErrIrregularTiming, rep.Summary())
}

// warn writes a WARN line to the prepare log and the progress output.
func (r *runner) warn(msg string) {
	fmt.Fprintf(r.log, "WARN: %s\n", msg)
	fmt.Fprintf(r.opts.Log, "%-11s WARN %s\n", "prepare", msg)
}

// caps probes the local ffmpeg's VideoToolbox support once per run; the
// encoder choice and the tonemap choice share the answer.
func (r *runner) caps() (media.HWCaps, error) {
	if r.hw == nil {
		c, err := r.opts.Tools.VideoToolboxCaps()
		if err != nil {
			return c, err
		}
		r.hw = &c
	}
	return *r.hw, nil
}

// encoderSig is the step-signature suffix for a re-encode. libx264 adds
// nothing, so stamps written before the encoder was selectable still match
// it; any other encoder re-runs a step that libx264 produced (and back).
// Hardware decode does not change the output and is not part of it.
func encoderSig(enc media.VideoEncode) string {
	if enc.EncoderName() == media.EncoderX264 {
		return ""
	}
	return ":" + enc.EncoderName()
}

// cfrSig is the step-signature suffix for an encode pinned to a constant
// rate. It changes every encode's signature from before encodes were
// constant frame rate, so such an output (which may keep a source's gaps)
// is encoded again.
func cfrSig(rate string) string { return ":cfr=" + rate }

// step runs fn unless output exists and the recorded stamp for name matches
// the current input (size+mtime) and sig. Progress is persisted after each
// step so a crash resumes.
func (r *runner) step(name, input, output, sig string, fn func() error) error {
	st, err := os.Stat(input)
	if err != nil {
		return fmt.Errorf("%s: input %s: %w", name, input, err)
	}
	outAbs := output
	if !filepath.IsAbs(outAbs) {
		outAbs = r.job.Path(output)
	}
	want := Stamp{Input: input, Size: st.Size(), Mtime: st.ModTime().Unix(), Sig: sig, Output: output}
	if have, ok := r.state.Steps[name]; ok {
		if _, oerr := os.Stat(outAbs); oerr == nil && have.Input == want.Input && have.Size == want.Size && have.Mtime == want.Mtime && have.Sig == want.Sig {
			fmt.Fprintf(r.opts.Log, "%-11s skip (unchanged) -> %s\n", name, output)
			return nil
		}
	}
	fmt.Fprintf(r.opts.Log, "%-11s run  -> %s\n", name, output)
	if err := os.MkdirAll(filepath.Dir(outAbs), 0o755); err != nil {
		return err
	}
	started := time.Now()
	if err := fn(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if _, err := os.Stat(outAbs); err != nil {
		return fmt.Errorf("%s: produced no output at %s", name, output)
	}
	want.At = time.Now().UTC().Format(time.RFC3339)
	r.state.Steps[name] = want
	fmt.Fprintf(r.opts.Log, "%-11s done in %s\n", name, time.Since(started).Round(time.Millisecond))
	return r.saveState()
}

func loadState(job *model.Job) *State {
	s := &State{Steps: map[string]Stamp{}}
	if job.Extra == nil {
		return s
	}
	raw, ok := job.Extra["prepare"]
	if !ok {
		return s
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return s
	}
	var loaded State
	if err := json.Unmarshal(b, &loaded); err != nil || loaded.Steps == nil {
		return s
	}
	return &loaded
}

func (r *runner) saveState() error {
	b, err := json.Marshal(r.state)
	if err != nil {
		return err
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		return err
	}
	if r.job.Extra == nil {
		r.job.Extra = map[string]any{}
	}
	r.job.Extra["prepare"] = generic
	return r.job.Save()
}
