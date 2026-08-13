// Package srclog extracts log message templates from Go source code and
// matches runtime log lines back to them.
package srclog

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Placeholder marks a variable part of a template (drain-style convention).
const Placeholder = "<*>"

// Location is a call site that produces a template.
type Location struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Template is one log message template extracted from source. No regex is
// stored: matchers (in any language) derive the pattern from Template and the
// manifest's Anchor — QuoteMeta the literal parts, join with one capture
// group per placeholder, anchor per the manifest. Storing it was tried and
// produced manifests whose regex contradicted their anchor.
type Template struct {
	ID        string     `json:"id"`
	Template  string     `json:"template"`
	Level     string     `json:"level,omitempty"`
	Lib       string     `json:"lib,omitempty"`
	Locations []Location `json:"locations,omitempty"`
}

// Stats summarizes an extraction run.
type Stats struct {
	Files       int `json:"files"`
	Calls       int `json:"calls"`
	Dynamic     int `json:"dynamic"`
	ParseErrors int `json:"parse_errors"`
}

// Manifest is the artifact produced by extraction and consumed by matchers.
//
// Anchor selects the matching semantics: "" matches whole lines (^...$);
// "suffix" matches error dictionaries — the template must end the string and
// start either at its beginning or after ": ", Go's error-wrapping boundary,
// so "pq: <*>" fires inside "handler: load user 42: pq: ...".
type Manifest struct {
	Version   int         `json:"version"`
	Module    string      `json:"module,omitempty"`
	Commit    string      `json:"commit,omitempty"`
	Anchor    string      `json:"anchor,omitempty"`
	Stats     Stats       `json:"stats"`
	Templates []*Template `json:"templates"`
}

// templateID returns the stable identity of a template: first 12 hex chars of
// sha256(level + NUL + template). Call sites are attributes, not identity.
func templateID(level, template string) string {
	h := sha256.Sum256([]byte(level + "\x00" + template))
	return hex.EncodeToString(h[:6])
}

// verbRe matches printf verbs: %[flags][argnum][width][.precision]verb.
var verbRe = regexp.MustCompile(`%[#+\-0 ']*(\[\d+\])?(\*|\d+)?(\.(\*|\d+)?)?[a-zA-Z%]`)

// normalizeFormat rewrites a printf format string into a template:
// every verb becomes Placeholder, %% becomes a literal %.
func normalizeFormat(format string) string {
	return verbRe.ReplaceAllStringFunc(format, func(m string) string {
		if m == "%%" {
			return "%"
		}
		return Placeholder
	})
}

// regexFor builds the whole-line matching pattern for a template, or "" if
// the template has no placeholders (match by exact string instead).
func regexFor(template string) string {
	if !strings.Contains(template, Placeholder) {
		return ""
	}
	return "^" + regexBody(template) + "$"
}

// regexForSuffix builds the dictionary-entry pattern: anchored at the end,
// starting at the string start or after ": " (the error-wrapping boundary).
func regexForSuffix(template string) string {
	return `(?:^|: )` + regexBody(template) + "$"
}

func regexBody(template string) string {
	parts := strings.Split(template, Placeholder)
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return strings.Join(parts, "(.*?)")
}
