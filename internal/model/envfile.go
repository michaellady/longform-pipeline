package model

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// EnvFileVar names the file the setup wizard writes secrets to
// (KEY='value' lines). launchd cannot source a file, so the binary loads it
// itself at startup; variables already present in the environment win.
const EnvFileVar = "LONGFORM_ENV_FILE"

// EnvFilePath returns the env file to load: $LONGFORM_ENV_FILE when set, else
// <ConfigRoot>/env.
func EnvFilePath() string {
	if v := os.Getenv(EnvFileVar); v != "" {
		return ExpandHome(v)
	}
	return filepath.Join(ConfigRoot(), "env")
}

// LoadEnvFile reads EnvFilePath into the process environment. It returns the
// names (never the values) of the variables it set. A missing file is not an
// error; a malformed line is skipped.
func LoadEnvFile() ([]string, error) {
	return loadEnvFile(EnvFilePath())
}

func loadEnvFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var set []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		key, val, ok := parseEnvLine(sc.Text())
		if !ok {
			continue
		}
		if _, present := os.LookupEnv(key); present {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return set, err
		}
		set = append(set, key)
	}
	return set, sc.Err()
}

// parseEnvLine accepts `KEY=value`, `KEY='value'` (with '\” for a literal
// quote, as the wizard writes), `KEY="value"`, and an optional `export ` prefix.
func parseEnvLine(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:eq])
	for i, r := range key {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')) {
			return "", "", false
		}
	}
	val = strings.TrimSpace(line[eq+1:])
	switch {
	case len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'':
		val = strings.ReplaceAll(val[1:len(val)-1], `'\''`, `'`)
	case len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"':
		val = strings.ReplaceAll(val[1:len(val)-1], `\"`, `"`)
	}
	return key, val, true
}
