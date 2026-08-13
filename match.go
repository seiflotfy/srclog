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
	// Suffix-matcher results only: the unmatched prefix (including its ": "
	// separator) before the point where the template took over. Keeping it
	// makes suffix matches lossless.
	Prefix string
	Suffix bool
}

// Matcher matches log lines against a manifest's templates.
type Matcher struct {
	exact map[string]*Template
	// Fuzzy templates are indexed by a boundary token. Line-anchored: the
	// first literal token (the word before any placeholder) — a line can only
	// match a template whose first token equals its own. Suffix-anchored: the
	// trailing literal token — a suffix match ends the string, so the line's
	// last token must equal the template's. rest holds templates with no
	// indexable token. Both lists stay in specificity order.
	byTok  map[string][]fuzzyTemplate
	rest   []fuzzyTemplate
	suffix bool // dictionary (suffix-anchored) semantics
	// byID/exactByID support MatchByID — single-template verification for
	// external routers (e.g. a drain-style prefilter).
	byID      map[string]*fuzzyTemplate
	exactByID map[string]*Template
}

type fuzzyTemplate struct {
	re  *regexp.Regexp
	lp  litParts
	t   *Template
	lit int // cached literalLen for merge ordering
}

// tryMatch runs one template against line: the literal-segment matcher when
// possible, the compiled regex when the template's literals contain '\n'. A
// line containing '\n' can only match the latter kind — the regex's '.'
// never matches newline, so a newline must be covered by a literal.
func (f *fuzzyTemplate) tryMatch(line string, hasNL, suffix bool) (*Match, bool) {
	if f.lp.ok {
		if hasNL {
			return nil, false
		}
		if suffix {
			if pre, params, ok := f.lp.matchSuffix(line); ok {
				return &Match{Template: f.t, Params: params, Prefix: pre, Suffix: true}, true
			}
			return nil, false
		}
		if params, ok := f.lp.matchLine(line); ok {
			return &Match{Template: f.t, Params: params}, true
		}
		return nil, false
	}
	if sub := f.re.FindStringSubmatch(line); sub != nil {
		if suffix {
			return &Match{Template: f.t, Params: sub[2:], Prefix: sub[1], Suffix: true}, true
		}
		return &Match{Template: f.t, Params: sub[1:]}, true
	}
	return nil, false
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
	mt := &Matcher{
		exact: make(map[string]*Template), byTok: map[string][]fuzzyTemplate{},
		suffix: suffix, byID: map[string]*fuzzyTemplate{}, exactByID: map[string]*Template{},
	}
	var fuzzy []fuzzyTemplate
	for i, t := range m.Templates {
		if t == nil {
			return nil, fmt.Errorf("manifest: null template at index %d", i)
		}
		// Superseded entries stay in the manifest (append-only catalogs) but
		// must not compete for matches — their alias target does the work.
		if _, superseded := m.Aliases[t.ID]; superseded {
			continue
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
			mt.exactByID[t.ID] = t
			continue
		}
		// regexFor output is always syntactically valid, but regexp still
		// rejects oversized programs — a pathological manifest must be a
		// bounded error, not a panic.
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", t.ID, err)
		}
		fuzzy = append(fuzzy, fuzzyTemplate{re: re, lp: litFor(t.Template), t: t, lit: literalLen(t.Template)})
	}
	// Most literal text first, so "dial <*>: timeout" beats "dial <*>: <*>".
	sort.SliceStable(fuzzy, func(i, j int) bool { return fuzzy[i].lit > fuzzy[j].lit })
	for _, f := range fuzzy {
		tok, ok := "", false
		if suffix {
			tok, ok = lastLiteralToken(f.t.Template)
		} else {
			tok, ok = firstLiteralToken(f.t.Template)
		}
		if ok {
			mt.byTok[tok] = append(mt.byTok[tok], f)
		} else {
			mt.rest = append(mt.rest, f)
		}
	}
	for i := range mt.rest {
		mt.byID[mt.rest[i].t.ID] = &mt.rest[i]
	}
	for _, fs := range mt.byTok {
		for i := range fs {
			mt.byID[fs[i].t.ID] = &fs[i]
		}
	}
	return mt, nil
}

// firstLiteralToken returns the template's first whitespace-delimited token
// when it contains no placeholder — a line can only match such a template if
// its own first token is identical.
func firstLiteralToken(template string) (string, bool) {
	fields := strings.Fields(template)
	if len(fields) == 0 || strings.Contains(fields[0], Placeholder) {
		return "", false
	}
	return fields[0], true
}

func literalLen(template string) int {
	return len(template) - strings.Count(template, Placeholder)*len(Placeholder)
}

// Match returns the best template for line, or ok=false.
//
// Line-anchored matching scans only templates sharing the line's first token
// plus the unindexed rest, merged in specificity order. Suffix (dictionary)
// matchers have everything in rest — a flat scan, fine at dictionary sizes.
func (m *Matcher) Match(line string) (*Match, bool) {
	if t, ok := m.exact[line]; ok {
		return &Match{Template: t}, true
	}
	var cand []fuzzyTemplate
	if m.suffix {
		cand = m.byTok[lastTokenOf(line)]
	} else if tok, ok := firstTokenOf(line); ok {
		cand = m.byTok[tok]
	}
	hasNL := strings.IndexByte(line, '\n') >= 0
	i, j := 0, 0
	for i < len(cand) || j < len(m.rest) {
		var f *fuzzyTemplate
		if j >= len(m.rest) || (i < len(cand) && cand[i].lit >= m.rest[j].lit) {
			f, i = &cand[i], i+1
		} else {
			f, j = &m.rest[j], j+1
		}
		if mt, ok := f.tryMatch(line, hasNL, m.suffix); ok {
			return mt, true
		}
	}
	return nil, false
}

// MatchByID runs a single template — named by catalog ID — against line,
// with exactly Match's semantics for that template. It is the verification
// half of an external router (a prefilter proposes an ID, MatchByID checks
// it and extracts params). ok=false when the template doesn't match or the
// ID is unknown.
func (m *Matcher) MatchByID(id, line string) (*Match, bool) {
	if f, ok := m.byID[id]; ok {
		return f.tryMatch(line, strings.IndexByte(line, '\n') >= 0, m.suffix)
	}
	if t, ok := m.exactByID[id]; ok && line == t.Template {
		return &Match{Template: t}, true
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
