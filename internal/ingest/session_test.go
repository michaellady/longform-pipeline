package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// TestLaunchSessionModel runs the test binary as a stub claude and checks the
// argv it was started with: --model resolves from the option, then
// $LONGFORM_CLAUDE_MODEL, then "opus", and job.extra.session_model records it.
func TestLaunchSessionModel(t *testing.T) {
	cases := []struct {
		name, env, opt, want string
	}{
		{name: "default", want: "opus"},
		{name: "env", env: "sonnet", want: "sonnet"},
		{name: "option_wins_over_env", env: "sonnet", opt: "claude-opus-5-5", want: "claude-opus-5-5"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupEnv(t)
			argvFile := filepath.Join(t.TempDir(), "argv.json")
			t.Setenv("LONGFORM_TEST_HELPER", "")
			t.Setenv(claudeArgvEnv, argvFile)
			t.Setenv(ClaudeEnv, os.Args[0])
			t.Setenv(ClaudeModelEnv, tc.env)

			j := stagedJob(t, "evc", strings.Repeat(string(rune('a'+i)), 64), time.Now())
			for _, s := range []model.State{model.StatePrepared, model.StateUploaded, model.StateClipped} {
				if err := j.Advance(s, "test", ""); err != nil {
					t.Fatal(err)
				}
			}
			launched, err := LaunchSession(j, LaunchOptions{Model: tc.opt})
			if err != nil || !launched {
				t.Fatalf("launched=%v err=%v", launched, err)
			}

			argv := waitArgv(t, argvFile)
			t.Logf("claude argv: %q", argv)
			want := []string{"-p", "/longform-pipeline:longform-publish " + j.Dir(), "--model", tc.want, "--permission-mode", "acceptEdits"}
			if len(argv) < len(want) || strings.Join(argv[:len(want)], "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("argv prefix:\n got %q\nwant %q", argv, want)
			}
			if n := strings.Count(strings.Join(argv, "\x00")+"\x00", "--model\x00"); n != 1 {
				t.Errorf("--model appears %d times in %q", n, argv)
			}

			re, err := model.LoadJob(j.Dir())
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := re.Extra[SessionModelKey].(string); got != tc.want {
				t.Errorf("extra.%s = %v, want %q", SessionModelKey, re.Extra[SessionModelKey], tc.want)
			}
		})
	}
}

// waitArgv polls for the detached stub's argv file.
func waitArgv(t *testing.T, path string) []string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			var argv []string
			if err := json.Unmarshal(b, &argv); err != nil {
				t.Fatalf("argv file: %v", err)
			}
			return argv
		}
		if time.Now().After(deadline) {
			t.Fatalf("stub claude never wrote %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
