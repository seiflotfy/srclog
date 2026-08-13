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
	exact  map[string]*Template
	fuzzy  []fuzzyTemplate
	suffix bool // dictionary (suffix-anchored) semantics
}

type fuzzyTemplate struct {
	re *regexp.Regexp
	t  *Template
}

// NewMatcher compiles a manifest into a matcher. Patterns are derived from
// each Template string; the manifest's Anchor field selects whole-line or
// suffix (dictionary) semantics.
func NewMatcher(m *Manifest) (*Matcher, error) {
	suffix := false
	switch m.Anchor {
	case "":
	case "suffix":
		suffix = true
	default:
		return nil, fmt.Errorf("manifest: unknown anchor %q", m.Anchor)
	}
	mt := &Matcher{exact: make(map[string]*Template), suffix: suffix}
	for i, t := range m.Templates {
		if t == nil {
			return nil, fmt.Errorf("manifest: null template at index %d", i)
		}
		// A template with no literal text compiles to a match-everything
		// pattern — a silent catch-all classifier. The extractors never emit
		// one, but manifests are external input.
		if isZeroLiteral(t.Template) {
			return nil, fmt.Errorf("template %s: no literal text (would match everything)", t.ID)
		}
		var pattern string
		if suffix {
			// Exact-map shortcuts don't apply mid-string; every dictionary
			// entry is a regex. Dictionaries are small, so this is fine.
			pattern = regexForSuffix(t.Template)
		} else {
			pattern = regexFor(t.Template)
		}
		if pattern == "" {
			mt.exact[t.Template] = t
			continue
		}
		// regexFor output is always syntactically valid, but regexp still
		// rejects oversized programs — a pathological manifest must be a
		// bounded error, not a panic.
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", t.ID, err)
		}
		mt.fuzzy = append(mt.fuzzy, fuzzyTemplate{re: re, t: t})
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
