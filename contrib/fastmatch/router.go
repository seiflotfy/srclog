// Package fastmatch puts a drain3 token prefilter in front of srclog's
// matcher. srclog templates over-approximate to drain token shapes (any
// token containing a placeholder becomes a wildcard position); at match
// time drain3's SIMD-grade tokenizer and prefix tree propose one shape in
// ~hundreds of nanoseconds, srclog's MatchByID verifies the shape's
// candidates exactly, and anything the fast path can't decide — params
// containing spaces change the token count, shapes can collide, long lines
// exceed drain's bounds — falls back to the full srclog scan. Coverage is
// therefore identical to srclog.Matcher.Match by construction; only speed
// changes.
//
// This module is deliberately separate from the srclog root module, which
// stays dependency-free.
package fastmatch

import (
	"sort"
	"strings"

	"github.com/axiomhq/drain3"
	"github.com/bits-and-blooms/bitset"
	"github.com/seiflotfy/srclog"
)

// Router matches lines against a manifest via a drain3 prefilter with a
// verified fallback. Like a drain3 Matcher, a Router is not safe for
// concurrent use; give each goroutine its own.
type Router struct {
	m     *srclog.Matcher
	d     *drain3.Matcher
	cands [][]string // drain template ID → srclog template IDs, most literal first
}

// New builds a Router for a whole-line manifest (Anchor must be ""; suffix
// dictionaries keep their own index inside srclog).
func New(man *srclog.Manifest) (*Router, error) {
	m, err := srclog.NewMatcher(man)
	if err != nil {
		return nil, err
	}
	cfg := drain3.DefaultConfig()
	cfg.ParametrizeNumericTokens = false // shapes are exact; only placeholders wildcard

	type shape struct {
		toks   []string
		params *bitset.BitSet
		n      int
		ids    []string
		lits   []int
	}
	shapes := map[string]*shape{}
	var order []string
	for _, t := range man.Templates {
		if _, superseded := man.Aliases[t.ID]; superseded {
			continue
		}
		toks := strings.Split(t.Template, " ")
		if len(toks) >= cfg.MaxTokens || len(t.Template) >= cfg.MaxBytes {
			continue // unreachable via the fast path; the fallback covers it
		}
		params := bitset.New(uint(len(toks)))
		key := make([]string, len(toks))
		var dense []string
		for i, tok := range toks {
			if strings.Contains(tok, srclog.Placeholder) {
				params.Set(uint(i))
				key[i] = "\x01"
			} else {
				dense = append(dense, tok)
				key[i] = tok
			}
		}
		k := strings.Join(key, "\x00")
		s := shapes[k]
		if s == nil {
			s = &shape{toks: dense, params: params, n: len(toks)}
			shapes[k] = s
			order = append(order, k)
		}
		s.ids = append(s.ids, t.ID)
		s.lits = append(s.lits, literalLen(t.Template))
	}

	r := &Router{m: m, cands: make([][]string, len(order)+1)}
	dts := make([]drain3.Template, 0, len(order))
	for i, k := range order {
		s := shapes[k]
		// Mirror srclog's specificity order inside a shape.
		sort.SliceStable(s.ids, func(a, b int) bool { return s.lits[a] > s.lits[b] })
		dts = append(dts, drain3.Template{
			ID:         i + 1,
			Tokens:     s.toks,
			Params:     s.params,
			TokenCount: s.n,
			Count:      1,
		})
		r.cands[i+1] = s.ids
	}
	d, err := drain3.NewMatcherFromTemplates(cfg, dts)
	if err != nil {
		return nil, err
	}
	r.d = d
	return r, nil
}

func literalLen(template string) int {
	return len(template) - strings.Count(template, srclog.Placeholder)*len(srclog.Placeholder)
}

// Match behaves like the underlying srclog Matcher's Match: same coverage,
// same params. On lines where more than one template matches, the router
// may prefer a different (still exactly matching) template than the full
// scan's specificity order — the fallback triggers whenever the fast path
// finds nothing.
func (r *Router) Match(line string) (*srclog.Match, bool) {
	if id, ok := r.d.MatchID(line); ok && id < len(r.cands) {
		for _, tid := range r.cands[id] {
			if mt, ok := r.m.MatchByID(tid, line); ok {
				return mt, true
			}
		}
	}
	return r.m.Match(line)
}

// Matcher returns the underlying srclog matcher (for direct use or as the
// primary in a srclog.Resolver).
func (r *Router) Matcher() *srclog.Matcher { return r.m }
