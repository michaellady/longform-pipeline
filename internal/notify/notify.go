// Package notify fans a job event out to macOS Notification Center and Slack.
// Nothing here may fail the caller: Send returns the joined errors for logging
// and every command that calls it ignores the result.
package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// WebhookEnv is the environment variable holding the Slack incoming-webhook
// URL. The value is a secret: never print it.
const WebhookEnv = "LONGFORM_SLACK_WEBHOOK"

// OsascriptEnv overrides the osascript binary (tests point it at a stub).
const OsascriptEnv = "LONGFORM_OSASCRIPT"

var httpClient = &http.Client{Timeout: 10 * time.Second}

// Send delivers one event. profile and job may be nil (the CLI passes what it
// could load). It always appends to <job>/logs/events.log first so the audit
// trail exists even when no channel is configured.
func Send(p *model.Profile, j *model.Job, event, message string) error {
	var errs []error
	if j != nil {
		if err := appendEvent(j, event, message); err != nil {
			errs = append(errs, err)
		}
		if j.ID != "" && !strings.Contains(message, j.ID) {
			if message == "" {
				message = j.ID
			} else {
				message = j.ID + ": " + message
			}
		}
	}
	title := "longform"
	if p != nil && p.Name != "" {
		title += " " + p.Name
	}
	if p != nil && p.Notify.MacOS {
		if err := macOS(title, event, message); err != nil {
			errs = append(errs, fmt.Errorf("macos: %w", err))
		}
	}
	if url := os.Getenv(WebhookEnv); url != "" {
		if err := slack(url, fmt.Sprintf("[%s] %s: %s", title, event, message)); err != nil {
			errs = append(errs, fmt.Errorf("slack: %w", err))
		}
	}
	return errors.Join(errs...)
}

func appendEvent(j *model.Job, event, message string) error {
	p := j.Path(filepath.Join("logs", "events.log"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339), event, strings.ReplaceAll(message, "\n", " "))
	return err
}

func macOS(title, subtitle, message string) error {
	bin := os.Getenv(OsascriptEnv)
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("osascript"); err != nil {
			bin = "/usr/bin/osascript"
		}
	}
	script := fmt.Sprintf("display notification %s with title %s subtitle %s", asString(message), asString(title), asString(subtitle))
	cmd := exec.Command(bin, "-e", script)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// asString quotes a value as an AppleScript string literal.
func asString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func slack(url, text string) error {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}
