package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

func setupEnv(t *testing.T) *model.Job {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LONGFORM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("LONGFORM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("LONGFORM_LOG_DIR", filepath.Join(root, "logs"))
	t.Setenv(WebhookEnv, "")
	t.Setenv(OsascriptEnv, "")
	j, err := model.NewJob("evc", strings.Repeat("a", 64), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestSendSlackAndEventsLog(t *testing.T) {
	j := setupEnv(t)
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
	}))
	defer srv.Close()
	t.Setenv(WebhookEnv, srv.URL)
	p := &model.Profile{Name: "evc"}
	if err := Send(p, j, "staged", "1 source(s)"); err != nil {
		t.Fatal(err)
	}
	text := got["text"]
	for _, want := range []string{"[longform evc]", "staged", j.ID, "1 source(s)"} {
		if !strings.Contains(text, want) {
			t.Errorf("slack text %q lacks %q", text, want)
		}
	}
	b, err := os.ReadFile(j.Path(filepath.Join("logs", "events.log")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\tstaged\t1 source(s)") {
		t.Errorf("events.log = %q", b)
	}
}

func TestSendReportsFailuresWithoutPanicking(t *testing.T) {
	j := setupEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	t.Setenv(WebhookEnv, srv.URL)
	t.Setenv(OsascriptEnv, filepath.Join(t.TempDir(), "missing-osascript"))
	err := Send(&model.Profile{Name: "evc", Notify: model.NotifyConfig{MacOS: true}}, j, "clipped", "x")
	if err == nil || !strings.Contains(err.Error(), "slack") || !strings.Contains(err.Error(), "macos") {
		t.Fatalf("want both channel errors, got %v", err)
	}
	t.Setenv(WebhookEnv, "")
	if err := Send(nil, nil, "x", "y"); err != nil {
		t.Fatalf("nil profile and job must be fine: %v", err)
	}
}

func TestSendMacOSRunsOsascript(t *testing.T) {
	j := setupEnv(t)
	stub, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no `true` binary")
	}
	t.Setenv(OsascriptEnv, stub)
	if err := Send(&model.Profile{Name: "evc", Notify: model.NotifyConfig{MacOS: true}}, j, "drafted", `say "hi" \ bye`); err != nil {
		t.Fatal(err)
	}
	if got := asString(`say "hi" \ bye`); got != `"say \"hi\" \\ bye"` {
		t.Errorf("asString = %s", got)
	}
}
