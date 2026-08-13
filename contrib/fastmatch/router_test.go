package fastmatch

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/seiflotfy/srclog"
)

// benchCorpus mirrors the root module's benchmark workload (bench_test.go):
// zipf-skewed template frequencies, realistic param pools, ~20% of params
// carrying spaces (the drain-router hard case), 0.1% unmatched residual.
func benchCorpus(tb testing.TB, nLines int) (*srclog.Manifest, []string) {
	rng := rand.New(rand.NewSource(42))
	man := &srclog.Manifest{Version: 1}
	addT := func(level, tpl string) {
		man.Templates = append(man.Templates, &srclog.Template{
			ID: fmt.Sprintf("%s-%03d", level, len(man.Templates)), Template: tpl, Level: level,
		})
	}
	for i := 0; i < 60; i++ {
		addT("info", fmt.Sprintf("service %02d ready, accepting connections", i))
	}
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
	for i := 0; i < 20; i++ {
		addT("error", fmt.Sprintf("failed to acquire lock%02d<*>", i))
		addT("info", fmt.Sprintf("<*> finished stage%02d", i))
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
	}
	zipf := rand.NewZipf(rng, 1.3, 4, uint64(len(man.Templates)-1))

	var lines []string
	for len(lines) < nLines {
		if len(lines)%1000 == 999 {
			lines = append(lines, fmt.Sprintf("garbage %d line that matches nothing at all %08x", rng.Intn(1000), rng.Uint32()))
			continue
		}
		tpl := man.Templates[zipf.Uint64()].Template
		out := tpl
		for strings.Contains(out, srclog.Placeholder) {
			var p string
			switch rng.Intn(5) {
			case 0:
				p = ips[rng.Intn(len(ips))]
			case 1:
				p = uuids[rng.Intn(len(uuids))]
			case 2:
				p = fmt.Sprintf("%d", rng.Intn(100000))
			case 3:
				p = fmt.Sprintf("%dms", rng.Intn(5000))
			case 4:
				p = "load user " + uuids[rng.Intn(len(uuids))] + ": " + errs[rng.Intn(len(errs))]
			}
			out = strings.Replace(out, srclog.Placeholder, p, 1)
		}
		lines = append(lines, out)
	}
	return man, lines
}

// render reconstructs the line a whole-line match describes; whole-line
// matches are lossless, so this must reproduce the input byte for byte.
func render(m *srclog.Match) string {
	parts := strings.Split(m.Template.Template, srclog.Placeholder)
	var b strings.Builder
	b.WriteString(parts[0])
	for i, p := range m.Params {
		b.WriteString(p)
		b.WriteString(parts[i+1])
	}
	return b.String()
}

// TestRouterCoverage: the router must match exactly the lines the full scan
// matches, and every router match must be lossless (a genuinely matching
// template). Template choice may differ only on ambiguous lines; count and
// report the divergence rate.
func TestRouterCoverage(t *testing.T) {
	man, lines := benchCorpus(t, 20000)
	r, err := New(man)
	if err != nil {
		t.Fatal(err)
	}
	full, err := srclog.NewMatcher(man)
	if err != nil {
		t.Fatal(err)
	}
	diverged := 0
	for _, line := range lines {
		rm, rok := r.Match(line)
		fm, fok := full.Match(line)
		if rok != fok {
			t.Fatalf("coverage differs on %q: router=%v full=%v", line, rok, fok)
		}
		if !rok {
			continue
		}
		if got := render(rm); got != line {
			t.Fatalf("router match not lossless on %q: rendered %q via %s", line, got, rm.Template.ID)
		}
		if rm.Template.ID != fm.Template.ID {
			diverged++
		}
	}
	t.Logf("template-choice divergence: %d/%d lines", diverged, len(lines))
	if diverged > len(lines)/100 {
		t.Fatalf("divergence too high: %d/%d", diverged, len(lines))
	}
}

func BenchmarkFullScanMatch(b *testing.B) {
	man, lines := benchCorpus(b, 20000)
	m, err := srclog.NewMatcher(man)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Match(lines[i%len(lines)])
	}
}

func BenchmarkRouterMatch(b *testing.B) {
	man, lines := benchCorpus(b, 20000)
	r, err := New(man)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Match(lines[i%len(lines)])
	}
}

// The adversarial regime for srclog's first-token index: many templates
// sharing one first token, so the full scan walks a huge bucket while the
// router's prefix tree discriminates on later tokens.
func sharedTokenCorpus(tb testing.TB) (*srclog.Manifest, []string) {
	man := &srclog.Manifest{Version: 1}
	for i := 0; i < 500; i++ {
		man.Templates = append(man.Templates, &srclog.Template{
			ID:       fmt.Sprintf("req-%03d", i),
			Template: fmt.Sprintf("request handler%03d returned <*> in <*> for route%03d", i, i),
			Level:    "info",
		})
	}
	rng := rand.New(rand.NewSource(9))
	var lines []string
	for i := 0; i < 5000; i++ {
		n := rng.Intn(500)
		lines = append(lines, fmt.Sprintf("request handler%03d returned %d in %dms for route%03d", n, rng.Intn(600), rng.Intn(900), n))
	}
	return man, lines
}

func BenchmarkSharedTokenFullScan(b *testing.B) {
	man, lines := sharedTokenCorpus(b)
	m, err := srclog.NewMatcher(man)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := m.Match(lines[i%len(lines)]); !ok {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkSharedTokenRouter(b *testing.B) {
	man, lines := sharedTokenCorpus(b)
	r, err := New(man)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := r.Match(lines[i%len(lines)]); !ok {
			b.Fatal("expected match")
		}
	}
}
