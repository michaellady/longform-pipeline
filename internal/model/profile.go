package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Profile is everything brand-specific. The plugin ships no profile; the
// setup wizard writes one per brand under ProfilesDir(). See
// docs/profile-schema.md for field semantics.
type Profile struct {
	Name        string           `json:"name"`
	Brand       Brand            `json:"brand"`
	Inbox       string           `json:"inbox"`
	Photos      PhotosConfig     `json:"photos"`
	Orientation string           `json:"orientation"` // "portrait" | "landscape"
	Stitch      StitchConfig     `json:"stitch"`
	Prepare     PrepareConfig    `json:"prepare"`
	Stabilize   StabilizeConfig  `json:"stabilize"`
	Censor      CensorConfig     `json:"censor"`
	Transcribe  TranscribeConfig `json:"transcribe"`
	OpusClip    OpusClipConfig   `json:"opusclip"`
	Cadence     map[string]int   `json:"cadence"` // platform -> posts per day
	SlotsLocal  []string         `json:"slots_local"`
	Timezone    string           `json:"timezone"`
	BacklogDays int              `json:"backlog_days"`
	CTA         CTAConfig        `json:"cta"`
	Copy        CopyConfig       `json:"copy"`
	Buffer      BufferConfig     `json:"buffer"`
	Statics     StaticsConfig    `json:"statics"`
	Notify      NotifyConfig     `json:"notify"`
	Extra       map[string]any   `json:"extra,omitempty"`
}

type Brand struct {
	Display string `json:"display"`
	Site    string `json:"site"`
}

// PhotosConfig is the second feed: recordings the phone tags into an Apple
// Photos album arrive through iCloud Photos, and the watcher clones each
// original into a job once it exists on this Mac.
type PhotosConfig struct {
	Enabled bool   `json:"enabled"`
	Album   string `json:"album"` // Photos album name the phone adds recordings to
	// DownloadMissing asks Photos to fetch an original that iCloud has not
	// downloaded yet (needs Photos access for the watcher). Off, the watcher
	// waits for Photos' own "Download Originals to this Mac" to deliver it.
	DownloadMissing bool `json:"download_missing"`
	// Osxphotos is the executable path; empty = LONGFORM_OSXPHOTOS, PATH,
	// ~/.local/bin, /opt/homebrew/bin.
	Osxphotos string `json:"osxphotos,omitempty"`
	// Library overrides the system Photos library path (osxphotos --library).
	Library string `json:"library,omitempty"`
}

type StitchConfig struct {
	Enabled          bool `json:"enabled"`
	SessionWindowMin int  `json:"session_window_min"`
}

// PrepareConfig tunes the prepare stage's video re-encodes (the HDR tonemap
// and the stabilise pass).
type PrepareConfig struct {
	// Encoder is "auto" (default: Apple's hardware H.264 encoder when this
	// Mac's ffmpeg can use it, libx264 otherwise), "videotoolbox" (require
	// the hardware encoder; prepare fails without it) or "x264" (always
	// libx264 CRF 18 with software decode: slower, larger files).
	Encoder string `json:"encoder"`
}

// Values for PrepareConfig.Encoder.
const (
	PrepareEncoderAuto         = "auto"
	PrepareEncoderVideoToolbox = "videotoolbox"
	PrepareEncoderX264         = "x264"
)

type StabilizeConfig struct {
	Enabled   bool `json:"enabled"`
	Shakiness int  `json:"shakiness"` // vidstabdetect shakiness 1..10
	Smoothing int  `json:"smoothing"` // vidstabtransform smoothing frames
	Zoom      int  `json:"zoom"`      // percent
}

type CensorConfig struct {
	Enabled bool     `json:"enabled"`
	Mode    string   `json:"mode"` // "beep" | "mute"
	Extra   []string `json:"extra"`
}

type TranscribeConfig struct {
	Model    string `json:"model"`    // path to a ggml model; empty = default
	Language string `json:"language"` // e.g. "en"
	Threads  int    `json:"threads"`
}

type OpusClipConfig struct {
	TemplateID   string            `json:"template_id"`
	StripOverlay bool              `json:"strip_overlay"`
	Shorts       CurationConfig    `json:"shorts"`
	Full         FullRenderConfig  `json:"full"`
	Accounts     map[string]string `json:"accounts"` // platform key -> postAccountId
	SubAccounts  map[string]string `json:"sub_accounts,omitempty"`
}

type CurationConfig struct {
	Model        string `json:"model"` // "ClipBasic" | "ClipAnything"
	Prompt       string `json:"prompt,omitempty"`
	Durations    []int  `json:"durations"`
	Aspect       string `json:"aspect"`
	RemoveFiller bool   `json:"remove_filler"`
}

type FullRenderConfig struct {
	SkipCurate bool   `json:"skip_curate"`
	Aspect     string `json:"aspect"`
}

type CTAConfig struct {
	LeadMagnets         map[string]string `json:"lead_magnets"`  // name -> url
	CommentToDM         map[string]string `json:"comment_to_dm"` // platform -> keyword
	UTMSourceByPlatform bool              `json:"utm_source_by_platform"`
}

type CopyConfig struct {
	Style       string `json:"style"`
	VoiceCorpus string `json:"voice_corpus,omitempty"`
	Boilerplate string `json:"boilerplate,omitempty"`
}

type BufferConfig struct {
	Channels map[string]string `json:"channels"` // platform -> channel id
	Tags     map[string]string `json:"tags"`     // format key -> tag id
}

// BufferChannel is the Buffer channel id for a cell platform: the exact key
// (linkedin_page), else its family, the part before the first "_"
// (linkedin), else "". parse-decision fills static cells with it and the
// gate checks them against it, so both read the profile the same way.
func (p *Profile) BufferChannel(platform string) string {
	if p == nil {
		return ""
	}
	if id := p.Buffer.Channels[platform]; id != "" {
		return id
	}
	if fam, _, ok := strings.Cut(platform, "_"); ok {
		return p.Buffer.Channels[fam]
	}
	return ""
}

type StaticsConfig struct {
	TemplatesDir string   `json:"templates_dir"`
	Formats      []string `json:"formats"`
}

type NotifyConfig struct {
	SlackChannel string `json:"slack_channel,omitempty"`
	MacOS        bool   `json:"macos"`
}

// ErrNoProfile is returned when the named profile file does not exist.
var ErrNoProfile = errors.New("profile not found")

// ProfilePath returns the file a profile is read from.
func ProfilePath(name string) string { return filepath.Join(ProfilesDir(), name+".json") }

// LoadProfile reads and validates a profile by name.
func LoadProfile(name string) (*Profile, error) {
	b, err := os.ReadFile(ProfilePath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoProfile, ProfilePath(name))
	}
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("profile %s: %w", name, err)
	}
	if p.Name == "" {
		p.Name = name
	}
	p.applyDefaults()
	return &p, p.Validate()
}

// ListProfiles returns the names of every profile on disk.
func ListProfiles() ([]string, error) {
	entries, err := os.ReadDir(ProfilesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		names = append(names, e.Name()[:len(e.Name())-5])
	}
	return names, nil
}

func (p *Profile) applyDefaults() {
	if p.Orientation == "" {
		p.Orientation = "portrait"
	}
	if p.Stitch.Enabled && p.Stitch.SessionWindowMin == 0 {
		p.Stitch.SessionWindowMin = 90
	}
	if p.Prepare.Encoder == "" {
		p.Prepare.Encoder = PrepareEncoderAuto
	}
	if p.Censor.Mode == "" {
		p.Censor.Mode = "beep"
	}
	if p.Transcribe.Language == "" {
		p.Transcribe.Language = "en"
	}
	if len(p.OpusClip.Shorts.Durations) == 0 {
		p.OpusClip.Shorts.Durations = []int{30, 60, 90}
	}
	if p.OpusClip.Shorts.Model == "" {
		p.OpusClip.Shorts.Model = "ClipBasic"
	}
	if p.OpusClip.Shorts.Aspect == "" {
		p.OpusClip.Shorts.Aspect = "portrait"
	}
	if p.OpusClip.Full.Aspect == "" {
		p.OpusClip.Full.Aspect = p.Orientation
	}
	if len(p.SlotsLocal) == 0 {
		p.SlotsLocal = []string{"09:00", "12:00", "15:00", "18:00", "21:00"}
	}
	if p.Timezone == "" {
		p.Timezone = "America/Los_Angeles"
	}
	if p.BacklogDays == 0 {
		p.BacklogDays = 25
	}
	if p.Cadence == nil {
		p.Cadence = map[string]int{"youtube": 2, "instagram": 1, "linkedin": 1, "tiktok": 2, "facebook": 1}
	}
	p.Inbox = ExpandHome(p.Inbox)
	p.Statics.TemplatesDir = ExpandHome(p.Statics.TemplatesDir)
	p.Copy.VoiceCorpus = ExpandHome(p.Copy.VoiceCorpus)
	p.Transcribe.Model = ExpandHome(p.Transcribe.Model)
	p.Photos.Osxphotos = ExpandHome(p.Photos.Osxphotos)
	p.Photos.Library = ExpandHome(p.Photos.Library)
}

// Validate checks the fields the transport layer cannot run without.
func (p *Profile) Validate() error {
	var errs []error
	if p.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if p.Inbox == "" {
		errs = append(errs, errors.New("inbox is required"))
	}
	if p.Orientation != "portrait" && p.Orientation != "landscape" {
		errs = append(errs, fmt.Errorf("orientation must be portrait or landscape, got %q", p.Orientation))
	}
	switch p.Prepare.Encoder {
	case "", PrepareEncoderAuto, PrepareEncoderVideoToolbox, PrepareEncoderX264:
	default:
		errs = append(errs, fmt.Errorf("prepare.encoder must be auto, videotoolbox or x264, got %q", p.Prepare.Encoder))
	}
	if p.Censor.Mode != "beep" && p.Censor.Mode != "mute" {
		errs = append(errs, fmt.Errorf("censor.mode must be beep or mute, got %q", p.Censor.Mode))
	}
	switch p.OpusClip.Shorts.Model {
	case "ClipBasic", "ClipAnything":
	default:
		errs = append(errs, fmt.Errorf("opusclip.shorts.model must be ClipBasic or ClipAnything, got %q", p.OpusClip.Shorts.Model))
	}
	for k, v := range p.Cadence {
		if v < 0 {
			errs = append(errs, fmt.Errorf("cadence.%s must be >= 0", k))
		}
	}
	return errors.Join(errs...)
}
