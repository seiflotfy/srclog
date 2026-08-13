package srclog

import (
	"regexp"
	"strings"
)

// Rust extraction: tracing/log macro call sites (info!, error!, warn!,
// debug!, trace!). No Rust parser dependency — macro grammar at the call
// site is regular enough for a small scanner: find the macro, then take the
// first top-level string literal that isn't a named-argument value
// (target: "...", name: "..."). Rust {}-format specs normalize to <*>.

var rustMacroRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(trace|debug|info|warn|error|event)!\s*\(`)

// eventLevelRe pulls the Level::X argument out of event!(Level::WARN, ...).
var eventLevelRe = regexp.MustCompile(`^\s*(?:tracing::)?Level::(TRACE|DEBUG|INFO|WARN|ERROR)`)

// scanRust extracts templates from one Rust source file. Macro sites are
// located on a masked copy (string and comment contents blanked — doc
// comments are full of macro-shaped examples); literals are read from the
// original at the same offsets.
func scanRust(src, file string, byKey map[string]*Template, stats *Stats) {
	masked := blankRust(src)
	// line numbers by offset
	lineAt := func(off int) int { return 1 + strings.Count(src[:off], "\n") }

	for _, m := range rustMacroRe.FindAllStringSubmatchIndex(masked, -1) {
		level := src[m[2]:m[3]]
		open := m[1] - 1 // index of '('
		if level == "event" {
			// event!(Level::WARN, ...) — dynamic-level form
			if lm := eventLevelRe.FindStringSubmatch(src[open+1:]); lm != nil {
				level = strings.ToLower(lm[1])
			} else {
				level = "unknown"
			}
		}
		stats.Calls++
		lit, ok := firstMessageLiteral(src[open+1:])
		if !ok {
			stats.Dynamic++
			continue
		}
		tmpl := normalizeRustFormat(lit)
		if isZeroLiteral(tmpl) {
			stats.Dynamic++
			continue
		}
		recordAt(byKey, level, "tracing", tmpl, file, lineAt(m[2]))
	}
}

// firstMessageLiteral scans macro arguments for the first depth-0 string
// literal that isn't a named-argument value: preceded by ':' (target: "net")
// or '=' (tracing field syntax, scope = "datapoint"). Returns the unescaped
// literal.
func firstMessageLiteral(s string) (string, bool) {
	depth := 0
	lastNonSpace := byte(',')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '(', '[', '{':
			depth++
		case ']', '}':
			depth--
		case ')':
			if depth == 0 {
				return "", false // macro closed, no message literal
			}
			depth--
		case '/':
			if i+1 < len(s) && s[i+1] == '/' { // line comment
				for i < len(s) && s[i] != '\n' {
					i++
				}
				continue
			}
			if i+1 < len(s) && s[i+1] == '*' { // block comment
				if end := strings.Index(s[i+2:], "*/"); end >= 0 {
					i += 2 + end + 1
				} else {
					return "", false
				}
				continue
			}
		case '\'':
			// char literal ('a', '\n') vs lifetime ('a) — skip a closing
			// quote within 3 chars, otherwise treat as lifetime and move on.
			for j := i + 1; j < len(s) && j <= i+3; j++ {
				if s[j] == '\\' {
					j++
					continue
				}
				if j < len(s) && s[j] == '\'' {
					i = j
					break
				}
			}
		case 'r':
			// raw string r"..." / r#"..."#
			if lit, end, ok := rawString(s[i:]); ok {
				if depth == 0 && lastNonSpace != ':' && lastNonSpace != '=' {
					return lit, true
				}
				i += end - 1
				lastNonSpace = '"'
				continue
			}
		case '"':
			lit, end := quotedString(s[i:])
			if depth == 0 && lastNonSpace != ':' && lastNonSpace != '=' {
				return lit, true
			}
			i += end - 1
			lastNonSpace = '"'
			continue
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			lastNonSpace = c
		}
	}
	return "", false
}

// quotedString consumes a normal Rust string starting at s[0]=='"' and
// returns its unescaped content and the total consumed length.
func quotedString(s string) (string, int) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '0':
				b.WriteByte(0)
			default:
				b.WriteByte(s[i]) // \" \\ \' and anything exotic verbatim
			}
			continue
		}
		if c == '"' {
			return b.String(), i + 1
		}
		b.WriteByte(c)
	}
	return b.String(), len(s)
}

// rawString consumes r"..." / r#"..."# starting at s[0]=='r'.
func rawString(s string) (lit string, length int, ok bool) {
	i := 1
	hashes := 0
	for i < len(s) && s[i] == '#' {
		hashes++
		i++
	}
	if i >= len(s) || s[i] != '"' {
		return "", 0, false
	}
	close := `"` + strings.Repeat("#", hashes)
	end := strings.Index(s[i+1:], close)
	if end < 0 {
		return "", 0, false
	}
	return s[i+1 : i+1+end], i + 1 + end + len(close), true
}

// blankRust replaces the contents of strings and comments with spaces
// (newlines kept, so offsets and line numbers stay aligned).
func blankRust(src string) string {
	out := []byte(src)
	for i := 0; i < len(src); i++ {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			j := i
			for j < len(src) && src[j] != '\n' {
				out[j] = ' '
				j++
			}
			i = j - 1
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				end = len(src) - i - 2
			}
			for j := i; j < i+2+end+2 && j < len(src); j++ {
				if src[j] != '\n' {
					out[j] = ' '
				}
			}
			i += 2 + end + 1
		case src[i] == '"':
			_, n := quotedString(src[i:])
			for j := i + 1; j < i+n-1 && j < len(src); j++ {
				if src[j] != '\n' {
					out[j] = ' '
				}
			}
			i += n - 1
		case src[i] == 'r' && (strings.HasPrefix(src[i+1:], `"`) || strings.HasPrefix(src[i+1:], `#`)):
			if _, n, ok := rawString(src[i:]); ok {
				for j := i + 1; j < i+n && j < len(src); j++ {
					if src[j] != '\n' {
						out[j] = ' '
					}
				}
				i += n - 1
			}
		}
	}
	return string(out)
}

var rustSpecRe = regexp.MustCompile(`\{[^{}]*\}`)

// normalizeRustFormat rewrites Rust format strings into templates:
// {} {:?} {name} {0:>8} → <*>; {{ and }} → literal braces.
func normalizeRustFormat(f string) string {
	f = strings.ReplaceAll(f, "{{", "\x00")
	f = strings.ReplaceAll(f, "}}", "\x01")
	f = rustSpecRe.ReplaceAllString(f, Placeholder)
	f = strings.ReplaceAll(f, "\x00", "{")
	return strings.ReplaceAll(f, "\x01", "}")
}
