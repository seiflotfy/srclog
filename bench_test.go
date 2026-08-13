package srclog

import (
	"fmt"
	"math/rand"
	"testing"
)

// benchCorpus builds a deterministic synthetic workload shaped like the
// measured corpora: a few hundred templates with a zipf-ish frequency skew,
// params drawn from realistic pools (IPs, durations, UUID-ish tokens, enum
// codes), a slice of lines whose last param cascades into a suffix
// dictionary, and a small unmatched residual.
type benchCorpus struct {
	primary *Matcher
	dict    *Matcher
	lines   []string
}

func buildBenchCorpus(tb testing.TB, nLines int) *benchCorpus {
	rng := rand.New(rand.NewSource(42))

	man := &Manifest{Version: 1}
	addT := func(level, tpl string) {
		man.Templates = append(man.Templates, &Template{
			ID: templateID(level, tpl), Template: tpl, Level: level,
		})
	}
	// Exact templates (no placeholder) — the free path.
	for i := 0; i < 60; i++ {
		addT("info", fmt.Sprintf("service %02d ready, accepting connections", i))
	}
	// Token-aligned placeholder templates.
	for i := 0; i < 120; i++ {
		switch i % 4 {
		case 0:
			addT("info", fmt.Sprintf("svc%02d: connected to <*> in <*>ms", i))
		case 1:
			addT("info", fmt.Sprintf("processed batch <*> rows=<*> shard%02d", i))
		case 2:
			addT("warn", fmt.Sprintf("retrying request <*> to backend%02d after <*>", i))
		case 3:
			addT("error", fmt.Sprintf("worker%02d failed: <*>", i))
		}
	}
	// Sub-token placeholders and leading-placeholder shapes.
	for i := 0; i < 20; i++ {
		addT("error", fmt.Sprintf("failed to acquire lock%02d<*>", i))
		addT("info", fmt.Sprintf("<*> finished stage%02d", i))
	}

	dman := &Manifest{Version: 1, Anchor: "suffix"}
	addD := func(tpl string) {
		dman.Templates = append(dman.Templates, &Template{
			ID: templateID("error", tpl), Template: tpl, Level: "error", Lib: "bench",
		})
	}
	addD("pq: deadlock detected")
	addD("context deadline exceeded")
	for i := 0; i < 200; i++ {
		switch i % 3 {
		case 0:
			addD(fmt.Sprintf("lib%02d: error code <*>", i))
		case 1:
			addD(fmt.Sprintf("lib%02d: connection refused by <*>", i))
		case 2:
			addD(fmt.Sprintf("lib%02d: request timed out", i))
		}
	}

	primary, err := NewMatcher(man)
	if err != nil {
		tb.Fatal(err)
	}
	dict, err := NewMatcher(dman)
	if err != nil {
		tb.Fatal(err)
	}

	ips := make([]string, 32)
	for i := range ips {
		ips[i] = fmt.Sprintf("10.0.%d.%d:5432", rng.Intn(256), rng.Intn(256))
	}
	uuids := make([]string, 64)
	for i := range uuids {
		uuids[i] = fmt.Sprintf("%08x-%04x-%04x", rng.Uint32(), rng.Intn(0xffff), rng.Intn(0xffff))
	}
	errs := []string{
		"pq: deadlock detected",
		"context deadline exceeded",
		"lib03: error code 429",
		"lib12: error code 503",
		"lib01: connection refused by 10.0.0.9:6379",
		"lib05: request timed out",
	}

	// zipf-ish: low template indices dominate, like prod's 129-template head.
	zipf := rand.NewZipf(rng, 1.3, 4, uint64(len(man.Templates)-1))

	lines := make([]string, 0, nLines)
	for len(lines) < nLines {
		switch {
		case len(lines)%1000 == 999: // ~0.1% unmatched residual
			lines = append(lines, fmt.Sprintf("garbage %d line that matches nothing at all %08x", rng.Intn(1000), rng.Uint32()))
			continue
		}
		t := man.Templates[zipf.Uint64()]
		tpl := t.Template
		var line string
		nParams := 0
		for i := 0; i < len(tpl); i++ {
			if i+len(Placeholder) <= len(tpl) && tpl[i:i+len(Placeholder)] == Placeholder {
				nParams++
				i += len(Placeholder) - 1
			}
		}
		if nParams == 0 {
			line = tpl
		} else {
			params := make([]any, nParams)
			for i := range params {
				switch rng.Intn(5) {
				case 0:
					params[i] = ips[rng.Intn(len(ips))]
				case 1:
					params[i] = uuids[rng.Intn(len(uuids))]
				case 2:
					params[i] = fmt.Sprintf("%d", rng.Intn(100000))
				case 3:
					params[i] = fmt.Sprintf("%dms", rng.Intn(5000))
				case 4:
					// ~20% of params end in a dictionary error → cascade work,
					// and they contain spaces (the drain-router hard case).
					params[i] = "load user " + uuids[rng.Intn(len(uuids))] + ": " + errs[rng.Intn(len(errs))]
				}
			}
			out := tpl
			for _, p := range params {
				out = replaceFirst(out, Placeholder, p.(string))
			}
			line = out
		}
		lines = append(lines, line)
	}
	return &benchCorpus{primary: primary, dict: dict, lines: lines}
}

func replaceFirst(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

func BenchmarkMatch(b *testing.B) {
	c := buildBenchCorpus(b, 20000)
	b.ReportAllocs()
	b.ResetTimer()
	hits := 0
	for i := 0; i < b.N; i++ {
		if _, ok := c.primary.Match(c.lines[i%len(c.lines)]); ok {
			hits++
		}
	}
	b.ReportMetric(float64(hits)/float64(b.N)*100, "hit%")
}

func BenchmarkResolve(b *testing.B) {
	c := buildBenchCorpus(b, 20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Resolve(c.primary, c.dict, c.lines[i%len(c.lines)])
	}
}

// BenchmarkMatchMiss is the worst case: every line walks the full candidate
// list and fails.
func BenchmarkMatchMiss(b *testing.B) {
	c := buildBenchCorpus(b, 100)
	line := "svc00: this line shares a first token but matches nothing because it never ends right zzz"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := c.primary.Match(line); ok {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkDictParam is the cascade inner loop in isolation: one param string
// resolved against the suffix dictionary (hit and miss).
func BenchmarkDictParam(b *testing.B) {
	c := buildBenchCorpus(b, 100)
	b.Run("hit", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := c.dict.Match("load user 42: pq: deadlock detected"); !ok {
				b.Fatal("expected match")
			}
		}
	})
	b.Run("miss", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := c.dict.Match("10.0.3.7:5432"); ok {
				b.Fatal("unexpected match")
			}
		}
	})
}

func BenchmarkBuildColumn(b *testing.B) {
	c := buildBenchCorpus(b, 20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BuildColumn(c.lines, c.primary, c.dict)
	}
	b.ReportMetric(float64(len(c.lines)), "lines/op")
}
