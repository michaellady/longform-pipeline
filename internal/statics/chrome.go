package statics

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Width and Height are the Instagram 4:5 frame every static uses.
const (
	Width  = 1080
	Height = 1350
	// Chrome's new headless viewport is ~87px shorter than --window-size, so
	// the window is oversized and the screenshot cropped from the top-left.
	windowSlack = 100
)

// DefaultChrome is the macOS Google Chrome binary; override with LONGFORM_CHROME.
const DefaultChrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

// ChromePath returns the configured headless browser binary.
func ChromePath() string {
	if v := os.Getenv("LONGFORM_CHROME"); v != "" {
		return v
	}
	return DefaultChrome
}

// FFmpegPath returns the ffmpeg binary (LONGFORM_FFMPEG or PATH lookup).
func FFmpegPath() string {
	if v := os.Getenv("LONGFORM_FFMPEG"); v != "" {
		return v
	}
	return "ffmpeg"
}

// Renderer drives headless Chrome.
type Renderer struct {
	Chrome string
	// Work is a scratch dir for Chrome's profile; one per process so a
	// running desktop Chrome is never joined.
	Work string
}

// NewRenderer builds a Renderer with a private user-data-dir under work.
func NewRenderer(chrome, work string) (*Renderer, error) {
	if chrome == "" {
		chrome = ChromePath()
	}
	if st, err := os.Stat(chrome); err != nil || st.IsDir() {
		return nil, fmt.Errorf("headless Chrome not found at %s (set LONGFORM_CHROME)", chrome)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	return &Renderer{Chrome: chrome, Work: work}, nil
}

func (r *Renderer) baseArgs() []string {
	return []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		"--force-device-scale-factor=1",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--user-data-dir=" + filepath.Join(r.Work, "chrome-profile"),
	}
}

// Screenshot renders htmlPath to a Width x Height PNG at outPath.
func (r *Renderer) Screenshot(htmlPath, outPath string) error {
	raw := outPath + ".raw.png"
	defer os.Remove(raw)
	args := append(r.baseArgs(),
		"--window-size="+strconv.Itoa(Width)+","+strconv.Itoa(Height+windowSlack),
		"--default-background-color=00000000",
		"--screenshot="+raw,
		fileURL(htmlPath),
	)
	if err := r.run(args); err != nil {
		return err
	}
	return cropTopLeft(raw, outPath, Width, Height)
}

// PrintPDF renders htmlPath to a PDF at outPath. The HTML sets its own @page
// size; Chrome is told to honour it and drop the header and footer.
func (r *Renderer) PrintPDF(htmlPath, outPath string) error {
	args := append(r.baseArgs(),
		"--no-pdf-header-footer",
		"--print-to-pdf="+outPath,
		fileURL(htmlPath),
	)
	if err := r.run(args); err != nil {
		return err
	}
	if st, err := os.Stat(outPath); err != nil || st.Size() == 0 {
		return fmt.Errorf("chrome produced no PDF at %s", outPath)
	}
	return nil
}

func (r *Renderer) run(args []string) error {
	cmd := exec.Command(r.Chrome, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("chrome %s: %w\n%s", strings.Join(args[len(args)-2:], " "), err, tail(stderr.String(), 2000))
	}
	return nil
}

func fileURL(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	return "file://" + abs
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// cropTopLeft decodes a PNG, keeps the top-left w x h, and writes it out.
func cropTopLeft(in, out string, w, h int) error {
	f, err := os.Open(in)
	if err != nil {
		return fmt.Errorf("chrome produced no screenshot: %w", err)
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("decode %s: %w", in, err)
	}
	b := src.Bounds()
	if b.Dx() < w || b.Dy() < h {
		return fmt.Errorf("screenshot is %dx%d, smaller than %dx%d", b.Dx(), b.Dy(), w, h)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	o, err := os.Create(out)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(o, dst); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}

// PNGSize reads a PNG header and returns its dimensions.
func PNGSize(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// ExtractFrame writes one JPEG frame of video at ms to out via ffmpeg.
func ExtractFrame(ffmpeg, video string, ms int64, out string) error {
	if ffmpeg == "" {
		ffmpeg = FFmpegPath()
	}
	if _, err := os.Stat(video); err != nil {
		return fmt.Errorf("source video: %w", err)
	}
	ts := fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
	cmd := exec.Command(ffmpeg, "-y", "-loglevel", "error", "-ss", ts, "-i", video, "-frames:v", "1", "-q:v", "2", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg frame at %s: %w: %s", ts, err, tail(stderr.String(), 1000))
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		return fmt.Errorf("ffmpeg produced no frame at %s (timestamp past the end?)", ts)
	}
	return nil
}
