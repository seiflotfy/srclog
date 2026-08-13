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
	// Prefix is the unmatched text (with its ": " separator) before a
	// suffix-dictionary match took over; Suffix marks dictionary nodes.
	// Together they make every node lossless: original = Prefix + rendered
	// template.
	Prefix string `json:"prefix,omitempty"`
	Suffix bool   `json:"suffix,omitempty"`
	Params []any  `json:"params,omitempty"`
}

// Resolve matches line against primary (whole-line templates) and cascades
// every captured param — and, when the line itself is unmatched, the whole
// line — through dict, which must be suffix-anchored. A nil or non-suffix
// dict disables the cascade rather than misclassifying with line semantics.
func Resolve(primary, dict *Matcher, line string) (*Node, bool) {
	if dict != nil && !dict.suffix {
		dict = nil
	}
	return resolve(nil, primary, dict, line)
}

func resolve(r *Resolver, primary, dict *Matcher, line string) (*Node, bool) {
	if m, ok := primary.Match(line); ok {
		return node(r, dict, m, maxCascadeDepth), true
	}
	// Bare-printed errors (log.Error(err)) have no source template but often
	// end in a well-known service error.
	if dict != nil {
		if m, ok := dict.Match(line); ok {
			return node(r, dict, m, maxCascadeDepth), true
		}
	}
	return nil, false
}

func node(r *Resolver, dict *Matcher, m *Match, depth int) *Node {
	n := &Node{
		ID:       m.Template.ID,
		Level:    m.Template.Level,
		Lib:      m.Template.Lib,
		Template: m.Template.Template,
		Prefix:   m.Prefix,
		Suffix:   m.Suffix,
	}
	for _, p := range m.Params {
		if dict != nil && depth > 0 {
			// Sprint-style templates capture the joining space into the param
			// ("failed<*>" → " pq: ..."), which would break the dictionary's
			// start-or-": " boundary. Trim left for matching, but keep the
			// trimmed whitespace in the child's prefix — nodes stay lossless.
			trimmed := strings.TrimLeft(p, " \t")
			if child, ok := resolveParam(r, dict, trimmed, depth); ok {
				if ws := p[:len(p)-len(trimmed)]; ws != "" {
					// Cached children are shared; clone before touching Prefix.
					c2 := *child
					c2.Prefix = ws + c2.Prefix
					child = &c2
				}
				n.Params = append(n.Params, child)
				continue
			}
		}
		n.Params = append(n.Params, p)
	}
	return n
}

// resolveParam matches one left-trimmed param against the dictionary,
// memoizing top-level results (hits and misses — the miss is the expensive
// scan) in the Resolver when one is present. Cached nodes are shared;
// callers clone before mutating.
func resolveParam(r *Resolver, dict *Matcher, trimmed string, depth int) (*Node, bool) {
	memo := r != nil && depth == maxCascadeDepth
	if memo {
		if child, ok := r.params[trimmed]; ok {
			return child, child != nil
		}
	}
	var child *Node
	if dm, ok := dict.Match(trimmed); ok {
		child = node(r, dict, dm, depth-1)
	}
	if memo {
		if len(r.params) >= resolverCacheMax {
			r.params = map[string]*Node{}
		}
		r.params[trimmed] = child
	}
	return child, child != nil
}

// resolverCacheMax bounds each memo map. On overflow the map is dropped
// wholesale — the hot set rebuilds immediately and the behavior stays
// deterministic.
const resolverCacheMax = 1 << 16

// Resolver is Resolve with memoization: full lines and top-level params
// each get a bounded cache, which pays off exactly where real traffic
// repeats — duplicate lines inside a block, and params (enum values, IPs,
// error strings) that recur across rows.
//
// Returned nodes are shared between calls and must be treated as immutable.
// A Resolver is not safe for concurrent use; give each goroutine its own.
type Resolver struct {
	primary, dict *Matcher
	lines         map[string]*Node // nil value = cached miss
	params        map[string]*Node // nil value = cached miss
}

// NewResolver wraps primary and dict (same contract as Resolve: dict must be
// suffix-anchored or nil) with fresh caches.
func NewResolver(primary, dict *Matcher) *Resolver {
	if dict != nil && !dict.suffix {
		dict = nil
	}
	return &Resolver{
		primary: primary, dict: dict,
		lines: map[string]*Node{}, params: map[string]*Node{},
	}
}

// Resolve is equivalent to the package-level Resolve over the wrapped
// matchers. The returned node is shared with other calls that saw the same
// line — treat it as immutable.
func (r *Resolver) Resolve(line string) (*Node, bool) {
	if n, ok := r.lines[line]; ok {
		return n, n != nil
	}
	n, _ := resolve(r, r.primary, r.dict, line)
	if len(r.lines) >= resolverCacheMax {
		r.lines = map[string]*Node{}
	}
	r.lines[line] = n
	return n, n != nil
}
