package srclog

import "strings"

// maxCascadeDepth bounds recursion through dictionary matches (wrapped errors,
// grpc desc fields). Real chains are 2-3 deep; the cap is a safety rail.
const maxCascadeDepth = 4

// Node is one level of a cascaded match. Params entries are either a plain
// string (no dictionary hit) or a *Node (the param itself matched).
type Node struct {
	ID       string `json:"id"`
	Level    string `json:"level,omitempty"`
	Lib      string `json:"lib,omitempty"`
	Template string `json:"template"`
	Params   []any  `json:"params,omitempty"`
}

// Resolve matches line against primary (whole-line templates) and cascades
// every captured param — and, when the line itself is unmatched, the whole
// line — through dict, which must be suffix-anchored. A nil or non-suffix
// dict disables the cascade rather than misclassifying with line semantics.
func Resolve(primary, dict *Matcher, line string) (*Node, bool) {
	if dict != nil && !dict.suffix {
		dict = nil
	}
	if m, ok := primary.Match(line); ok {
		return node(dict, m, maxCascadeDepth), true
	}
	// Bare-printed errors (log.Error(err)) have no source template but often
	// end in a well-known service error.
	if dict != nil {
		if m, ok := dict.Match(line); ok {
			return node(dict, m, maxCascadeDepth), true
		}
	}
	return nil, false
}

func node(dict *Matcher, m *Match, depth int) *Node {
	n := &Node{
		ID:       m.Template.ID,
		Level:    m.Template.Level,
		Lib:      m.Template.Lib,
		Template: m.Template.Template,
	}
	for _, p := range m.Params {
		if dict != nil && depth > 0 {
			// Sprint-style templates capture the joining space into the param
			// ("failed<*>" → " pq: ..."), which would break the dictionary's
			// start-or-": " boundary. Trim before matching.
			if dm, ok := dict.Match(strings.TrimSpace(p)); ok {
				n.Params = append(n.Params, node(dict, dm, depth-1))
				continue
			}
		}
		n.Params = append(n.Params, p)
	}
	return n
}
