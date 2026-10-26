package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfilePrepareEncoder(t *testing.T) {
	t.Setenv("LONGFORM_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(ProfilesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, prepare string) {
		t.Helper()
		body := `{"inbox":"/tmp/in"` + prepare + `}`
		if err := os.WriteFile(filepath.Join(ProfilesDir(), name+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("absent", "")
	p, err := LoadProfile("absent")
	if err != nil {
		t.Fatal(err)
	}
	if p.Prepare.Encoder != PrepareEncoderAuto {
		t.Errorf("default prepare.encoder = %q, want auto", p.Prepare.Encoder)
	}
	for _, v := range []string{PrepareEncoderAuto, PrepareEncoderVideoToolbox, PrepareEncoderX264} {
		write("ok", `,"prepare":{"encoder":"`+v+`"}`)
		if p, err := LoadProfile("ok"); err != nil || p.Prepare.Encoder != v {
			t.Errorf("prepare.encoder %q: %v %+v", v, err, p)
		}
	}
	write("bad", `,"prepare":{"encoder":"nvenc"}`)
	if _, err := LoadProfile("bad"); err == nil || !strings.Contains(err.Error(), "prepare.encoder") {
		t.Errorf("unknown encoder must be rejected, got %v", err)
	}
}
