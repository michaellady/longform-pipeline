//go:build darwin

package photos

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// On APFS, Clone is clonefile(2), not the copy fallback: the clone keeps the
// source's mode and mtime (copyFile writes 0644 and a fresh mtime).
func TestCloneUsesClonefileOnAPFS(t *testing.T) {
	dir := t.TempDir()
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		t.Fatal(err)
	}
	if name := strings.TrimRight(string(fs.Fstypename[:]), "\x00"); name != "apfs" {
		t.Skipf("temp dir is on %s, not APFS", name)
	}
	src := filepath.Join(dir, "src.mov")
	data := bytes.Repeat([]byte("clone me "), 100_000)
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(src, old, old); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "incoming", "dst.mov")
	if err := Clone(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dst); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("clone differs: %d bytes, %v", len(b), err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 || !st.ModTime().Equal(old) {
		t.Errorf("dst mode %v mtime %v: not a clonefile clone (want 0600, %v)", st.Mode().Perm(), st.ModTime(), old)
	}
	// The clone is independent: writing it leaves the source alone.
	if err := os.WriteFile(dst, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(src); !bytes.Equal(b, data) {
		t.Error("writing the clone changed the source")
	}
}
