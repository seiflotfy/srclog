package srclog

import (
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TestLitMatchDifferential pins the literal-segment matcher to the derived
// regex, capture for capture, over adversarial template/line pairs: adjacent
// placeholders, leading/trailing placeholders, sub-token placeholders,
// literals recurring inside params, ": " boundaries stacked in prefixes.
func TestLitMatchDifferential(t *testing.T) {
	templates := []string{
		"dial <*>: <*>",
		"failed to acquire lock<*>",
		"<*> finished stage",
		"a <*> b <*> c",
		"a<*>b<*>c",
		"x<*><*>y",
		"<*><*>",             // rejected by NewMatcher, still lit-vs-regex comparable? no literal — skip
		"end with <*>",
		"<*> starts it",
		"pq: <*>",
		"error code <*>",
		"a: b: <*> c",
		"repeat <*> repeat <*> repeat",
		": <*>",
		"only-literal-token",
		"sp  ace <*> double",
		"tab\ttok <*>",
	}
	lines := []string{
		"dial 10.0.0.1:5432: connection refused",
		"dial : ",
		"failed to acquire lock: pq: deadlock detected",
		"failed to acquire lock",
		"anything finished stage",
		" finished stage",
		"a  b  c",
		"a b c",
		"abc",
		"abbc",
		"aXbYcZbWc",
		"xZZy",
		"xy",
		"end with ",
		"end with x y z",
		"pq: deadlock detected",
		"handler: load user 42: pq: deadlock detected",
		"prefix: error code 429",
		"error code 429",
		"deep: deeper: deepest: error code 1",
		"a: b: q c",
		"a: b:  c",
		"repeat x repeat y repeat",
		"repeat repeat repeat repeat repeat",
		": x",
		"nope: : y",
		"only-literal-token",
		"sp  ace Q double",
		"tab\ttok Z",
		"",
		": ",
		"a: ",
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 400; i++ { // fuzz extra template/line pairs
		var b strings.Builder
		for j := 0; j < 1+rng.Intn(4); j++ {
			b.WriteString([]string{"a", "b", ": ", " ", "<*>", "xy", ":", "z "}[rng.Intn(8)])
		}
		templates = append(templates, b.String())
		var l strings.Builder
		for j := 0; j < rng.Intn(8); j++ {
			l.WriteString([]string{"a", "b", ": ", " ", "xy", ":", "z ", "Q"}[rng.Intn(8)])
		}
		lines = append(lines, l.String())
	}

	for _, tpl := range templates {
		if isZeroLiteral(tpl) || !strings.Contains(tpl, Placeholder) && tpl == "" {
			continue
		}
		lp := litFor(tpl)
		if !lp.ok {
			continue
		}
		lineRe := regexp.MustCompile("^" + regexBody(tpl) + "$")
		sufRe := regexp.MustCompile(regexForSuffix(tpl))
		for _, line := range lines {
			if strings.IndexByte(line, '\n') >= 0 {
				continue
			}
			// whole-line semantics
			wantSub := lineRe.FindStringSubmatch(line)
			gotParams, gotOK := lp.matchLine(line)
			if gotOK != (wantSub != nil) {
				t.Fatalf("line %q vs %q: lit ok=%v regex ok=%v", tpl, line, gotOK, wantSub != nil)
			}
			if gotOK && !sameParams(gotParams, wantSub[1:]) {
				t.Fatalf("line %q vs %q: lit params %q, regex %q", tpl, line, gotParams, wantSub[1:])
			}
			// suffix semantics
			wantSuf := sufRe.FindStringSubmatch(line)
			gotPre, gotSufParams, gotSufOK := lp.matchSuffix(line)
			if gotSufOK != (wantSuf != nil) {
				t.Fatalf("suffix %q vs %q: lit ok=%v regex ok=%v", tpl, line, gotSufOK, wantSuf != nil)
			}
			if gotSufOK {
				if gotPre != wantSuf[1] {
					t.Fatalf("suffix %q vs %q: lit prefix %q, regex %q", tpl, line, gotPre, wantSuf[1])
				}
				if !sameParams(gotSufParams, wantSuf[2:]) {
					t.Fatalf("suffix %q vs %q: lit params %q, regex %q", tpl, line, gotSufParams, wantSuf[2:])
				}
			}
		}
	}
}

func sameParams(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSuffixIndexEquivalence checks that the trailing-token index never
// changes what a suffix matcher returns: a matcher with the index against
// one with everything forced into the flat rest list.
func TestSuffixIndexEquivalence(t *testing.T) {
	man := &Manifest{Version: 1, Anchor: "suffix"}
	tpls := []string{
		"pq: deadlock detected",
		"pq: <*>",
		"context deadline exceeded",
		"error code <*>",
		"connection refused by <*>",
		"request timed out",
		"no such host",
		"i/o timeout",
		"lock<*>",
		"<*> not found",
	}
	for _, s := range tpls {
		man.Templates = append(man.Templates, &Template{ID: templateID("error", s), Template: s, Level: "error"})
	}
	indexed, err := NewMatcher(man)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := NewMatcher(man)
	if err != nil {
		t.Fatal(err)
	}
	// force the flat matcher to scan everything, preserving specificity order
	var all []fuzzyTemplate
	all = append(all, flat.rest...)
	for _, fs := range flat.byTok {
		all = append(all, fs...)
	}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].lit > all[i].lit {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	flat.byTok, flat.rest = map[string][]fuzzyTemplate{}, all

	lines := []string{
		"pq: deadlock detected",
		"handler: load user 42: pq: deadlock detected",
		"x: pq: something else",
		"error code 42",
		"prefix: error code 42",
		"request timed out",
		"deep: request timed out",
		"widget not found",
		"a: widget not found",
		"failed to acquire lock: table users",
		"10.0.0.1:5432",
		"unrelated line entirely",
		"",
	}
	for _, line := range lines {
		gm, gok := indexed.Match(line)
		wm, wok := flat.Match(line)
		if gok != wok {
			t.Fatalf("%q: indexed ok=%v flat ok=%v", line, gok, wok)
		}
		if gok && (gm.Template.ID != wm.Template.ID || gm.Prefix != wm.Prefix || !sameParams(gm.Params, wm.Params)) {
			t.Fatalf("%q: indexed %+v flat %+v", line, gm, wm)
		}
	}
}

// TestResolverEquivalence checks the memoizing Resolver returns node trees
// deep-equal to stateless Resolve over a corpus with heavy repetition.
func TestResolverEquivalence(t *testing.T) {
	c := buildBenchCorpus(t, 5000)
	r := NewResolver(c.primary, c.dict)
	for _, line := range c.lines {
		want, wok := Resolve(c.primary, c.dict, line)
		got, gok := r.Resolve(line)
		if gok != wok {
			t.Fatalf("%q: resolver ok=%v resolve ok=%v", line, gok, wok)
		}
		if gok && !reflect.DeepEqual(got, want) {
			t.Fatalf("%q:\nresolver %s\nresolve  %s", line, dump(got), dump(want))
		}
	}
}

func dump(n *Node) string { return fmt.Sprintf("%+v", *n) }

// TestMatchByID pins router verification to Match semantics.
func TestMatchByID(t *testing.T) {
	c := buildBenchCorpus(t, 2000)
	for _, line := range c.lines {
		if m, ok := c.primary.Match(line); ok {
			got, gok := c.primary.MatchByID(m.Template.ID, line)
			if !gok || got.Template.ID != m.Template.ID || !sameParams(got.Params, m.Params) {
				t.Fatalf("MatchByID disagrees on %q", line)
			}
		}
	}
	if _, ok := c.primary.MatchByID("nonexistent", "anything"); ok {
		t.Fatal("unknown ID matched")
	}
}
