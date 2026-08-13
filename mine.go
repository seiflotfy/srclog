package srclog

import (
	"sort"
	"strconv"
	"strings"
)

// Miner clusters unmatched log lines into candidate templates, drain-style:
// lines are bucketed by token count and first token, then merged into a
// cluster when at least simThreshold of tokens agree; disagreeing positions
// become placeholders. Candidates are proposals — they carry lib "mined" and
// must pass a promotion gate before joining a catalog (mining proposes, the
// catalog disposes).
type Miner struct {
	clusters map[string][]*cluster // key: tokenCount \x00 firstToken
	count    int
}

type cluster struct {
	tokens []string // Placeholder marks wildcard positions
	seen   int
}

// simThreshold is the fraction of token positions that must agree for a line
// to join a cluster. 0.5 is drain's conventional default.
const simThreshold = 0.5

func NewMiner() *Miner {
	return &Miner{clusters: make(map[string][]*cluster)}
}

// Add feeds one unmatched line to the miner.
func (mn *Miner) Add(line string) {
	tokens := strings.Fields(line)
	if len(tokens) == 0 {
		return
	}
	mn.count++
	// First token joins the key only if it looks constant; a leading variable
	// (an IP, a hash) would otherwise shatter clusters.
	first := tokens[0]
	if looksVariable(first) {
		first = Placeholder
	}
	key := strconv.Itoa(len(tokens)) + "\x00" + first
	best, bestSim := (*cluster)(nil), 0.0
	for _, c := range mn.clusters[key] {
		if sim := similarity(c.tokens, tokens); sim > bestSim {
			best, bestSim = c, sim
		}
	}
	if best == nil || bestSim < simThreshold {
		mn.clusters[key] = append(mn.clusters[key], &cluster{tokens: tokens, seen: 1})
		return
	}
	for i, t := range best.tokens {
		if t != tokens[i] {
			best.tokens[i] = Placeholder
		}
	}
	best.seen++
}

// similarity is the fraction of positions with equal tokens; existing
// placeholders count as matches.
func similarity(a, b []string) float64 {
	same := 0
	for i := range a {
		if a[i] == b[i] || a[i] == Placeholder {
			same++
		}
	}
	return float64(same) / float64(len(a))
}

// looksVariable reports whether a token is mostly digits/punctuation — the
// shapes (IPs, ids, hex, durations) that vary per line.
func looksVariable(tok string) bool {
	letters, other := 0, 0
	for _, r := range tok {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			letters++
		} else {
			other++
		}
	}
	return other > letters
}

// Candidates returns clusters seen at least minSeen times as templates,
// most-seen first. Zero-literal candidates are dropped — a cluster of pure
// placeholders explains nothing.
func (mn *Miner) Candidates(minSeen int) *Manifest {
	var ts []*Template
	seen := map[*Template]int{}
	for _, cs := range mn.clusters {
		for _, c := range cs {
			if c.seen < minSeen {
				continue
			}
			tmpl := strings.Join(c.tokens, " ")
			if isZeroLiteral(tmpl) {
				continue
			}
			t := &Template{ID: templateID("", tmpl), Template: tmpl, Lib: "mined"}
			ts = append(ts, t)
			seen[t] = c.seen
		}
	}
	sort.Slice(ts, func(i, j int) bool {
		if seen[ts[i]] != seen[ts[j]] {
			return seen[ts[i]] > seen[ts[j]]
		}
		return ts[i].Template < ts[j].Template
	})
	return &Manifest{Version: 1, Module: "mined", Templates: ts, Stats: Stats{Calls: mn.count}}
}
