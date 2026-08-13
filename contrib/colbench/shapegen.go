//go:build shapegen

package main

// shapegen: catalog-match a corpus and emit drain3-compatible token shapes.
// A shape = (catalog template, token count, which token positions fall inside
// param spans). Mixed tokens (literal glued to a param) become whole-token
// params — lossless, the arg absorbs the glue.
//
//	go run -tags shapegen shapegen.go manifest.json corpus.txt shapes.json

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/seiflotfy/srclog"
)

type shape struct {
	CatalogID  string   `json:"catalog_id"`
	Tokens     []string `json:"tokens"` // literal tokens only, dense
	ParamIdx   []int    `json:"param_idx"`
	TokenCount int      `json:"token_count"`
	Count      int      `json:"count"`

	// tracking for constant folding: one entry per position
	all     []string // token text per position (first line seen)
	isParam []bool
	varied  []bool // param positions whose value differed across lines
}

func main() {
	manifest, err := srclog.LoadManifest(os.Args[1])
	check(err)
	primary, err := srclog.NewMatcher(manifest)
	check(err)

	f, err := os.Open(os.Args[2])
	check(err)
	shapes := map[string]*shape{}
	matched, total := 0, 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		total++
		m, ok := primary.Match(line)
		if !ok {
			continue
		}
		matched++
		s := shapeOf(line, m)
		if s == nil {
			continue
		}
		key := fmt.Sprintf("%s|%d|%v", s.CatalogID, s.TokenCount, s.ParamIdx)
		if prev, ok := shapes[key]; ok {
			prev.Count++
			for i := range prev.all {
				if prev.isParam[i] && prev.all[i] != s.all[i] {
					prev.varied[i] = true
				}
			}
		} else {
			shapes[key] = s
		}
	}
	check(sc.Err())

	// Constant folding: a param position with a single distinct value across
	// the whole corpus becomes a literal token — catalog genericity doesn't
	// leak block-constant tokens into args.
	out := make([]*shape, 0, len(shapes))
	folded := 0
	for _, s := range shapes {
		s.Tokens = s.Tokens[:0]
		s.ParamIdx = s.ParamIdx[:0]
		for i := range s.all {
			if s.isParam[i] && s.varied[i] {
				s.ParamIdx = append(s.ParamIdx, i)
			} else {
				if s.isParam[i] {
					folded++
				}
				s.Tokens = append(s.Tokens, s.all[i])
			}
		}
		out = append(out, s)
	}
	fmt.Fprintf(os.Stderr, "shapegen: folded %d constant param positions\n", folded)
	data, err := json.Marshal(out)
	check(err)
	check(os.WriteFile(os.Args[3], data, 0o644))
	fmt.Fprintf(os.Stderr, "shapegen: %d/%d lines matched, %d distinct shapes\n", matched, total, len(out))
}

// shapeOf reconstructs the char spans of each param inside line, tokenizes on
// single spaces (drain3 semantics), and marks any token intersecting a param
// span as a param token.
func shapeOf(line string, m *srclog.Match) *shape {
	parts := strings.Split(m.Template.Template, srclog.Placeholder)
	if len(parts)-1 != len(m.Params) {
		return nil
	}
	type span struct{ s, e int }
	var spans []span
	pos := len(parts[0])
	for i, p := range m.Params {
		spans = append(spans, span{pos, pos + len(p)})
		pos += len(p) + len(parts[i+1])
	}

	toks := strings.Split(line, " ")
	if len(toks) > 64 { // drain3 MaxTokens default
		return nil
	}
	sh := &shape{CatalogID: m.Template.ID, TokenCount: len(toks), Count: 1,
		all: make([]string, len(toks)), isParam: make([]bool, len(toks)), varied: make([]bool, len(toks))}
	tpos := 0
	for i, tok := range toks {
		ts, te := tpos, tpos+len(tok)
		tpos = te + 1
		isParam := false
		for _, sp := range spans {
			if sp.s == sp.e { // empty capture: inside if it sits within the token
				if ts <= sp.s && sp.s < te {
					isParam = true
				}
			} else if ts < sp.e && sp.s < te {
				isParam = true
			}
		}
		sh.all[i] = tok
		sh.isParam[i] = isParam
		if isParam {
			sh.ParamIdx = append(sh.ParamIdx, i)
		} else {
			sh.Tokens = append(sh.Tokens, tok)
		}
	}
	return sh
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
