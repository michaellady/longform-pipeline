package cli

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/michaellady/longform-pipeline/internal/fakeopus"
)

func init() {
	Register(Command{Name: "fakeopus", Short: "Run the fake OpusClip API server for dry runs and tests", Run: runFakeOpus})
}

// runFakeOpus starts internal/fakeopus on --addr, prints the API URL on
// stdout (the value to export as OPUSCLIP_API_URL), and blocks until SIGINT
// or SIGTERM. The setup wizard's dry run uses it with OPUSCLIP_API_KEY=test.
func runFakeOpus(args []string) int {
	fs := flag.NewFlagSet("fakeopus", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:0", "listen address; port 0 picks a free port")
	key := fs.String("api-key", "test", "bearer token every /api request must present")
	delay := fs.Duration("delay", 0, "how long after project creation clips become exportable")
	dur := fs.Int("duration-sec", 300, "full-project clip length when the upload does not declare one")
	quiet := fs.Bool("quiet", false, "do not log requests to stderr")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	opts := fakeopus.Options{APIKey: *key, ProcessingDelay: *delay, DefaultDurationSec: *dur}
	if !*quiet {
		opts.Logf = func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	}
	s := fakeopus.New(opts)
	url, err := s.Start(*addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeopus:", err)
		return 70
	}
	fmt.Println(url)
	if !*quiet {
		fmt.Fprintf(os.Stderr, "fakeopus: listening; export OPUSCLIP_API_URL=%s OPUSCLIP_API_KEY=%s; Ctrl-C to stop\n", url, *key)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	_ = s.Close()
	time.Sleep(50 * time.Millisecond)
	return 0
}
