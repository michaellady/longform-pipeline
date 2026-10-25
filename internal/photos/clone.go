package photos

import (
	"io"
	"os"
	"path/filepath"
)

// Clone materialises src at dst: an APFS copy-on-write clone where the
// platform supports it (instant, no extra space), otherwise a byte copy. dst's
// directory is created; an existing dst is replaced.
func Clone(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	if err := cloneFile(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	tmp := dst + ".partial"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if n != st.Size() {
		_ = os.Remove(tmp)
		return io.ErrShortWrite
	}
	return os.Rename(tmp, dst)
}
