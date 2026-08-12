package srclog

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Match is a successful line-to-template match.
type Match struct {
	Template *Template
	// Params holds the text captured by each placeholder, in order.
	Params []string
}

// Matcher matches log lines against a manifest's templates.
type Matcher struct {
	exact map[string]*Template
	fuzzy []fuzzyTemplate
}

type fuzzyTemplate struct {
	re *regexp.Regexp
	t  *Template
}

// NewMatcher compiles a manifest into a matcher. Patterns are derived from
// each Template string — the manifest's stored regex is for non-Go consumers
// and is never trusted or read here.
func NewMatcher(m *Manifest) (*Matcher, error) {
	mt := &Matcher{exact: make(map[string]*Template)}
	for i, t := range m.Templates {
		if t == nil {
			return nil, fmt.Errorf("manifest: null template at index %d", i)
		}
		pattern := regexFor(t.Template)
		if pattern == "" {
			mt.exact[t.Template] = t
			continue
		}
		// regexFor output is QuoteMeta literals joined by (.*?): always compiles.
		mt.fuzzy = append(mt.fuzzy, fuzzyTemplate{re: regexp.MustCompile(pattern), t: t})
	}
	// Most literal text first, so "dial <*>: timeout" beats "dial <*>: <*>".
	sort.SliceStable(mt.fuzzy, func(i, j int) bool {
		return literalLen(mt.fuzzy[i].t.Template) > literalLen(mt.fuzzy[j].t.Template)
	})
	return mt, nil
}

func literalLen(template string) int {
	return len(template) - strings.Count(template, Placeholder)*len(Placeholder)
}

// Match returns the best template for line, or ok=false.
//
// ponytail: exact map + linear regex scan. Fine for per-repo template counts
// (hundreds); build a token trie if this ever sits on a hot ingest path.
func (m *Matcher) Match(line string) (*Match, bool) {
	if t, ok := m.exact[line]; ok {
		return &Match{Template: t}, true
	}
	for _, f := range m.fuzzy {
		if sub := f.re.FindStringSubmatch(line); sub != nil {
			return &Match{Template: f.t, Params: sub[1:]}, true
		}
	}
	return nil, false
}

// LoadManifest reads a manifest JSON file from disk.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported manifest version %d", path, m.Version)
	}
	return &m, nil
}
