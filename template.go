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

// Template is one log message template extracted from source.
type Template struct {
	ID       string `json:"id"`
	Template string `json:"template"`
	// Regex is an anchored pattern with one capture group per placeholder,
	// empty when the template has none (match by exact string). It exists for
	// non-Go consumers of the manifest; the Go Matcher derives its own
	// patterns from Template and ignores this field.
	Regex     string     `json:"regex,omitempty"`
	Level     string     `json:"level"`
	Lib       string     `json:"lib,omitempty"`
	Locations []Location `json:"locations"`
}

// Stats summarizes an extraction run.
type Stats struct {
	Files       int `json:"files"`
	Calls       int `json:"calls"`
	Dynamic     int `json:"dynamic"`
	ParseErrors int `json:"parse_errors"`
}

// Manifest is the artifact produced by extraction and consumed by matchers.
type Manifest struct {
	Version   int         `json:"version"`
	Module    string      `json:"module,omitempty"`
	Commit    string      `json:"commit,omitempty"`
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

// regexFor builds the anchored matching pattern for a template, or "" if the
// template has no placeholders.
func regexFor(template string) string {
	if !strings.Contains(template, Placeholder) {
		return ""
	}
	parts := strings.Split(template, Placeholder)
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return "^" + strings.Join(parts, "(.*?)") + "$"
}
