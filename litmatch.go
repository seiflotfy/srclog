package srclog

import "strings"

// litParts is the literal-segment form of a template: the text between
// placeholders, in order. It matches with exactly the semantics of the
// derived regex (lazy captures, '.' excluding newline) but without regexp's
// NFA execution — a prefix check, an end check, and one substring search per
// middle literal.
type litParts struct {
	segs []string
	ok   bool // false: a literal contains '\n'; matching must use the regex
}

func litFor(template string) litParts {
	segs := strings.Split(template, Placeholder)
	for _, s := range segs {
		if strings.IndexByte(s, '\n') >= 0 {
			return litParts{}
		}
	}
	return litParts{segs: segs, ok: true}
}

// matchLine matches line with regexFor semantics: ^L0(.*?)L1...(.*?)Lk$.
// Lazy captures make every choice forced-earliest: the final literal is
// pinned to the end by $, and taking each middle literal's earliest
// occurrence both reproduces the lazy captures and preserves completeness
// (an earlier choice only ever widens the window for what follows).
// Callers must ensure line contains no '\n' (the regex's '.' excludes it,
// and these literals contain none, so such a line cannot match).
func (lp litParts) matchLine(line string) ([]string, bool) {
	segs := lp.segs
	k := len(segs) - 1
	if k == 0 {
		return nil, line == segs[0]
	}
	if !strings.HasPrefix(line, segs[0]) {
		return nil, false
	}
	pos := len(segs[0])
	end := len(line) - len(segs[k])
	if end < pos || line[end:] != segs[k] {
		return nil, false
	}
	params := make([]string, 0, k)
	for i := 1; i < k; i++ {
		idx := strings.Index(line[pos:end], segs[i])
		if idx < 0 {
			return nil, false
		}
		params = append(params, line[pos:pos+idx])
		pos += idx + len(segs[i])
	}
	return append(params, line[pos:end]), true
}

// matchSuffix matches line with regexForSuffix semantics:
// ^((?:.*?: )??)body$ — the body must end the string and start either at the
// beginning or right after a ": " boundary, preferring the shortest prefix.
// Boundaries are tried left to right, which is exactly the lazy group's
// preference order.
func (lp litParts) matchSuffix(line string) (prefix string, params []string, ok bool) {
	off := 0
	for {
		if params, ok = lp.matchLine(line[off:]); ok {
			return line[:off], params, true
		}
		j := strings.Index(line[off:], ": ")
		if j < 0 {
			return "", nil, false
		}
		off += j + 2
	}
}

// asciiSpace mirrors the ASCII subset of unicode.IsSpace used by
// strings.Fields.
func asciiSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\v' || b == '\f' || b == '\r'
}

// firstTokenOf returns the line's first whitespace-delimited token without
// allocating. Any non-ASCII byte in or before the token falls back to
// strings.Fields so unicode whitespace splits identically to the index keys.
func firstTokenOf(line string) (string, bool) {
	i := 0
	for i < len(line) && asciiSpace(line[i]) {
		i++
	}
	j := i
	for j < len(line) && !asciiSpace(line[j]) {
		if line[j] >= 0x80 {
			f := strings.Fields(line)
			if len(f) == 0 {
				return "", false
			}
			return f[0], true
		}
		j++
	}
	if i == j {
		return "", false
	}
	return line[i:j], true
}

// lastTokenOf returns the text after the line's last ' ' byte — the lookup
// key for the suffix matcher's trailing-token index. Both sides of that
// index split on the single space byte, so the keys are consistent by
// construction.
func lastTokenOf(line string) string {
	return line[strings.LastIndexByte(line, ' ')+1:]
}

// lastLiteralToken returns the template's trailing token (the text after its
// last ' ') when that token is non-empty and placeholder-free. Because a
// suffix match must start at a token boundary (string start or after ": ")
// and captures can never inject text after the final literal, any line
// region the template matches ends with exactly this token — so a line whose
// last token differs can skip the template entirely.
func lastLiteralToken(template string) (string, bool) {
	w := template[strings.LastIndexByte(template, ' ')+1:]
	if w == "" || strings.Contains(w, Placeholder) {
		return "", false
	}
	return w, true
}
