package photos

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The test binary doubles as a fake osxphotos: with LONGFORM_PHOTOS_STUB=1 it
// records its argv and answers from files in LONGFORM_PHOTOS_STUB_DIR, so
// Client runs a real subprocess without a Photos library. Exported names here
// are test-only (export_test idiom) and shared with package photos_test.
const (
	stubEnv    = "LONGFORM_PHOTOS_STUB"
	stubDirEnv = "LONGFORM_PHOTOS_STUB_DIR"
)

func TestMain(m *testing.M) {
	if os.Getenv(stubEnv) == "1" {
		os.Exit(stubMain(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// stubMain answers one osxphotos call. Per subcommand <cmd>: <cmd>.stderr is
// written to stderr, <cmd>.exit is the exit code; query prints query.json;
// export copies export-<uuid>.src into its target dir as export-<uuid>.name
// and, like osxphotos when one photo fails, exits 0 having written nothing
// for a uuid without them.
func stubMain(args []string) int {
	dir := os.Getenv(stubDirEnv)
	if dir == "" || len(args) == 0 {
		return 64
	}
	if f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		b, _ := json.Marshal(args)
		f.Write(append(b, '\n'))
		f.Close()
	}
	if wd, err := os.Getwd(); err == nil {
		os.WriteFile(filepath.Join(dir, "cwd"), []byte(wd), 0o644)
	}
	cmd := args[0]
	if b, err := os.ReadFile(filepath.Join(dir, cmd+".stderr")); err == nil {
		os.Stderr.Write(b)
	}
	code := 0
	if b, err := os.ReadFile(filepath.Join(dir, cmd+".exit")); err == nil {
		code, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if code != 0 {
		return code
	}
	switch cmd {
	case "query":
		if b, err := os.ReadFile(filepath.Join(dir, "query.json")); err == nil {
			os.Stdout.Write(b)
		}
	case "export":
		if len(args) < 2 {
			return 64
		}
		uuid := ""
		for i, a := range args {
			if a == "--uuid" && i+1 < len(args) {
				uuid = args[i+1]
			}
		}
		src, err := os.ReadFile(filepath.Join(dir, "export-"+uuid+".src"))
		if err != nil {
			return 0 // osxphotos exits 0 when a photo fails to export
		}
		name, _ := os.ReadFile(filepath.Join(dir, "export-"+uuid+".name"))
		in, err := os.Open(string(src))
		if err != nil {
			return 1
		}
		defer in.Close()
		out, err := os.Create(filepath.Join(args[1], string(name)))
		if err != nil {
			return 1
		}
		if _, err := io.Copy(out, in); err != nil {
			return 1
		}
		if err := out.Close(); err != nil {
			return 1
		}
		// osxphotos leaves its export database next to the file.
		os.WriteFile(filepath.Join(args[1], ".osxphotos_export.db"), []byte("db"), 0o644)
	default:
		return 64
	}
	return 0
}

// FakeOsxphotos drives the stub from a test.
type FakeOsxphotos struct {
	Bin string // put in Client.Bin
	dir string
}

// NewFakeOsxphotos points the stub at a fresh answer directory; the album
// starts empty.
func NewFakeOsxphotos(t *testing.T) *FakeOsxphotos {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &FakeOsxphotos{Bin: bin, dir: t.TempDir()}
	t.Setenv(stubEnv, "1")
	t.Setenv(stubDirEnv, f.dir)
	f.SetAssets(t)
	return f
}

func (f *FakeOsxphotos) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// SetAssets sets what `osxphotos query --json` prints.
func (f *FakeOsxphotos) SetAssets(t *testing.T, assets ...map[string]any) {
	t.Helper()
	if assets == nil {
		assets = []map[string]any{}
	}
	b, err := json.Marshal(assets)
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "query.json", string(b))
}

// SetQueryStdout sets query's raw stdout.
func (f *FakeOsxphotos) SetQueryStdout(t *testing.T, stdout string) {
	t.Helper()
	f.write(t, "query.json", stdout)
}

// Fail makes cmd ("query" or "export") write stderr and exit with code.
func (f *FakeOsxphotos) Fail(t *testing.T, cmd, stderr string, code int) {
	t.Helper()
	f.write(t, cmd+".stderr", stderr)
	f.write(t, cmd+".exit", strconv.Itoa(code))
}

// ExportFrom makes `osxphotos export <dir> --uuid uuid` write src into dir
// as name.
func (f *FakeOsxphotos) ExportFrom(t *testing.T, uuid, src, name string) {
	t.Helper()
	f.write(t, "export-"+uuid+".src", src)
	f.write(t, "export-"+uuid+".name", name)
}

// Calls returns the argv of every stub invocation so far.
func (f *FakeOsxphotos) Calls(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatalf("calls.jsonl: %v", err)
		}
		calls = append(calls, args)
	}
	return calls
}

// FakeMovie is one album movie as osxphotos' JSON has it; an empty path is a
// null path with ismissing set (original still in iCloud).
func FakeMovie(uuid, originalFilename, path string, size int64) map[string]any {
	m := map[string]any{
		"uuid": uuid, "original_filename": originalFilename, "filename": strings.ToUpper(uuid) + ".mov",
		"path": nil, "ismissing": true, "incloud": true, "iscloudasset": true, "ismovie": true,
		"date": "2026-10-20T09:15:00-07:00", "date_added": "2026-10-20T09:20:00-07:00",
		"albums": []string{"longform-evc"}, "original_filesize": size,
	}
	if path != "" {
		m["path"], m["ismissing"] = path, false
	}
	return m
}
