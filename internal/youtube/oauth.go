package youtube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	yt "google.golang.org/api/youtube/v3"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// Scopes requested on first run. youtube.upload covers videos.insert;
// youtube.force-ssl covers thumbnails.set and later metadata edits (the same
// write scope the youtube-analytics tool already holds).
var Scopes = []string{yt.YoutubeUploadScope, yt.YoutubeForceSslScope}

// EnvClientSecret overrides the client secret path.
const EnvClientSecret = "LONGFORM_YT_CLIENT_SECRET"

// Time allowed for the user to finish the browser consent flow.
const authTimeout = 5 * time.Minute

// ConfigDir is <ConfigRoot>/youtube.
func ConfigDir() string { return filepath.Join(model.ConfigRoot(), "youtube") }

// ClientSecretPath is the installed-app OAuth client JSON downloaded from
// Google Cloud Console, or $LONGFORM_YT_CLIENT_SECRET when set.
func ClientSecretPath() string {
	if v := os.Getenv(EnvClientSecret); v != "" {
		return model.ExpandHome(v)
	}
	return filepath.Join(ConfigDir(), "client_secret.json")
}

// TokenPath holds the upload-scoped refresh token. Its contents are a
// credential and are never printed.
func TokenPath() string { return filepath.Join(ConfigDir(), "token-upload.json") }

// NewService returns an authenticated YouTube Data API client, running the
// browser consent flow on first use (or when the saved token cannot be
// refreshed).
func NewService(ctx context.Context) (*yt.Service, error) {
	client, err := httpClient(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := yt.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("youtube service: %w", err)
	}
	return svc, nil
}

func httpClient(ctx context.Context) (*http.Client, error) {
	secretPath := ClientSecretPath()
	b, err := os.ReadFile(secretPath)
	if err != nil {
		return nil, fmt.Errorf("reading OAuth client secret %s: %w\n\nTo set up:\n  1. Google Cloud Console -> APIs & Services -> Credentials\n  2. Create an OAuth 2.0 Client ID of type Desktop app (or reuse the one youtube-analytics uses)\n  3. Download the JSON and save it as %s (or point %s at it)\nSee docs/youtube.md.", secretPath, err, secretPath, EnvClientSecret)
	}
	cfg, err := google.ConfigFromJSON(b, Scopes...)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", secretPath, err)
	}

	tokenPath := TokenPath()
	tok, loadErr := loadToken(tokenPath)
	if loadErr == nil {
		src := cfg.TokenSource(ctx, tok)
		fresh, err := src.Token() // returns the saved token if still valid, else refreshes
		if err == nil {
			if fresh.AccessToken != tok.AccessToken {
				if err := saveToken(tokenPath, fresh); err != nil {
					fmt.Fprintf(os.Stderr, "yt-upload: warning: could not save refreshed token to %s: %v\n", tokenPath, err)
				}
			}
			return oauth2.NewClient(ctx, oauth2.ReuseTokenSource(fresh, src)), nil
		}
		fmt.Fprintf(os.Stderr, "yt-upload: saved token at %s could not be refreshed (%v); starting a new authorization\n", tokenPath, err)
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "yt-upload: ignoring unreadable token at %s: %v\n", tokenPath, loadErr)
	}

	tok, err = authorize(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := saveToken(tokenPath, tok); err != nil {
		return nil, fmt.Errorf("saving token to %s: %w", tokenPath, err)
	}
	fmt.Fprintf(os.Stderr, "yt-upload: authorization saved to %s\n", tokenPath)
	return cfg.Client(ctx, tok), nil
}

// authorize runs the installed-app flow with a loopback redirect: listen on an
// ephemeral 127.0.0.1 port, send the user to Google's consent page, and
// exchange the code that comes back.
func authorize(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting loopback listener: %w", err)
	}
	defer ln.Close()
	cfg.RedirectURL = fmt.Sprintf("http://%s/callback", ln.Addr().String())

	state, err := randomState()
	if err != nil {
		return nil, err
	}

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch; restart the command", http.StatusBadRequest)
			resCh <- result{err: errors.New("oauth: state mismatch on callback")}
			return
		}
		if e := q.Get("error"); e != "" {
			fmt.Fprintf(w, "<html><body><h1>Authorization failed</h1><p>%s</p></body></html>", e)
			resCh <- result{err: fmt.Errorf("oauth: %s", e)}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "no code in callback", http.StatusBadRequest)
			resCh <- result{err: errors.New("oauth: no code in callback")}
			return
		}
		fmt.Fprint(w, "<html><body><h1>Authorized</h1><p>You can close this tab and return to the terminal.</p></body></html>")
		resCh <- result{code: code}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	url := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Fprintln(os.Stderr, "yt-upload: first run, authorize in the browser. If it did not open, visit:")
	fmt.Fprintln(os.Stderr, url)
	fmt.Fprintln(os.Stderr)
	openBrowser(url)

	select {
	case r := <-resCh:
		if r.err != nil {
			return nil, r.err
		}
		tok, err := cfg.Exchange(ctx, r.code)
		if err != nil {
			return nil, fmt.Errorf("exchanging code: %w", err)
		}
		return tok, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(authTimeout):
		return nil, fmt.Errorf("oauth: no callback within %s; was the consent flow completed?", authTimeout)
	}
}

func randomState() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", fmt.Errorf("random state: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func loadToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t oauth2.Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// saveToken writes the token with owner-only permissions, atomically.
func saveToken(path string, t *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	tmp := path + ".tmp-" + hex.EncodeToString([]byte(time.Now().Format("150405.000")))
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return
	}
	_ = cmd.Start()
}
