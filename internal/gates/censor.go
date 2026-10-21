package gates

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultCensorPath is the plugin's strong-profanity list (D10), resolved
// relative to the binary: plugins/longform-pipeline/scripts/longform ->
// plugins/longform-pipeline/censor/default.json.
func DefaultCensorPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), "..", "censor", "default.json")
}

// LoadCensorList reads a word list. Accepted shapes: a JSON array of strings,
// or an object whose values are arrays of strings (every array is merged, so
// {"strong": [...], "slurs": [...]} works). Entries may be phrases.
func LoadCensorList(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var words []string
	if err := json.Unmarshal(raw, &words); err == nil {
		return normaliseWords(words), nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("%s: not a JSON array or object of word lists", path)
	}
	for _, v := range obj {
		var part []string
		if err := json.Unmarshal(v, &part); err == nil {
			words = append(words, part...)
		}
	}
	return normaliseWords(words), nil
}

func normaliseWords(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range in {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// Censor matches listed words as whole words, case-insensitively, in any
// text. A word boundary is anything that is not a letter or digit, so
// "damn!" and "#damn" match "damn" but "damnation" does not.
type Censor struct {
	words    []string
	patterns []*regexp.Regexp
}

// NewCensor compiles a list. Phrases are matched literally with the same
// whole-word boundaries at each end.
func NewCensor(words []string) *Censor {
	c := &Censor{}
	for _, w := range normaliseWords(words) {
		re := regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])` + regexp.QuoteMeta(w) + `(?:$|[^\p{L}\p{N}])`)
		c.words = append(c.words, w)
		c.patterns = append(c.patterns, re)
	}
	return c
}

// Len is the number of listed words.
func (c *Censor) Len() int {
	if c == nil {
		return 0
	}
	return len(c.words)
}

// Match returns the first listed word found in text, or "".
func (c *Censor) Match(text string) string {
	if c == nil || text == "" {
		return ""
	}
	for i, re := range c.patterns {
		if re.MatchString(text) {
			return c.words[i]
		}
	}
	return ""
}

// ErrNoCensorList is returned when neither the default file nor an explicit
// --censor-list exists.
var ErrNoCensorList = errors.New("censor list not found")

// ResolveCensor builds the gate's censor from the explicit path (if any),
// else the binary-relative default, plus the profile's extra words. A missing
// list is an error unless extra words were supplied, in which case the
// caller decides whether to continue with extras only.
func ResolveCensor(explicitPath string, extra []string) (*Censor, string, error) {
	path := explicitPath
	if path == "" {
		path = DefaultCensorPath()
	}
	var words []string
	var loadErr error
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			words, loadErr = LoadCensorList(path)
			if loadErr != nil {
				return nil, path, loadErr
			}
		} else {
			loadErr = fmt.Errorf("%w: %s", ErrNoCensorList, path)
		}
	} else {
		loadErr = ErrNoCensorList
	}
	words = append(words, extra...)
	return NewCensor(words), path, loadErr
}
