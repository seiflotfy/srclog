package srclog

// Size attribution for the tc02 encoding. Gated on SRCLOG_CORPUS +
// SRCLOG_MANIFEST (+ optional SRCLOG_DICTGLOB); zstd-9 via the system binary.
//
//	SRCLOG_CORPUS=lines.txt SRCLOG_MANIFEST=m.json SRCLOG_DICTGLOB='dicts/*.json' \
//	  go test -run TestColumnBreakdown -v .

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

func zsize(t *testing.T, data []byte) int {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "s")
	if err := os.WriteFile(in, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("zstd", "-f", "-q", "-9", in, "-o", in+".z").Run(); err != nil {
		t.Skipf("zstd unavailable: %v", err)
	}
	fi, err := os.Stat(in + ".z")
	if err != nil {
		t.Fatal(err)
	}
	return int(fi.Size())
}

func TestColumnBreakdown(t *testing.T) {
	corpus, manifest := os.Getenv("SRCLOG_CORPUS"), os.Getenv("SRCLOG_MANIFEST")
	if corpus == "" || manifest == "" {
		t.Skip("SRCLOG_CORPUS / SRCLOG_MANIFEST not set")
	}
	m, err := LoadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	primary, err := NewMatcher(m)
	if err != nil {
		t.Fatal(err)
	}
	var dict *Matcher
	if glob := os.Getenv("SRCLOG_DICTGLOB"); glob != "" {
		paths, _ := filepath.Glob(glob)
		merged := &Manifest{Version: 1, Anchor: "suffix"}
		for _, p := range paths {
			dm, err := LoadManifest(p)
			if err != nil {
				t.Fatal(err)
			}
			merged.Templates = append(merged.Templates, dm.Templates...)
		}
		if dict, err = NewMatcher(merged); err != nil {
			t.Fatal(err)
		}
	}

	f, err := os.Open(corpus)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	c := BuildColumn(lines, primary, dict)

	section := func(name string, fill func(*bytes.Buffer)) {
		var b bytes.Buffer
		fill(&b)
		t.Logf("%-28s raw %9d   zstd9 %8d", name, b.Len(), zsize(t, b.Bytes()))
	}
	section("rowIDs", func(b *bytes.Buffer) { writeUvarints(b, c.rowIDs) })
	section("template table", func(b *bytes.Buffer) {
		for _, tt := range c.table {
			writeString(b, tt.CatalogID)
			writeString(b, tt.Template)
		}
	})
	section("subIDs", func(b *bytes.Buffer) { writeUvarints(b, c.subIDs) })
	section("unmatched", func(b *bytes.Buffer) { writeStrings(b, c.unmatched) })

	type bs struct {
		k    bucketKey
		z    int
		raw  int
		vals int
	}
	var all []bs
	var dictable, plain bytes.Buffer
	for k, vals := range c.buckets {
		var b bytes.Buffer
		writeBucket(&b, vals)
		kind := b.Bytes()[0]
		if kind == 1 {
			dictable.Write(b.Bytes())
		} else {
			plain.Write(b.Bytes())
		}
		all = append(all, bs{k, zsize(t, b.Bytes()), b.Len(), len(vals)})
	}
	section("buckets (dict-encoded)", func(b *bytes.Buffer) { b.Write(dictable.Bytes()) })
	section("buckets (plain)", func(b *bytes.Buffer) { b.Write(plain.Bytes()) })

	sort.Slice(all, func(i, j int) bool { return all[i].z > all[j].z })
	t.Log("top buckets by compressed size:")
	for i, b := range all {
		if i >= 10 {
			break
		}
		tt := c.table[b.k.code-1]
		slot := fmt.Sprintf("slot %d", b.k.slot)
		if b.k.slot == -1 {
			slot = "prefix"
		}
		sample := ""
		if vals := c.buckets[b.k]; len(vals) > 0 {
			sample = vals[len(vals)/2]
			if len(sample) > 40 {
				sample = sample[:40]
			}
		}
		t.Logf("  z %8d (raw %8d, %6d vals) %-8s %.60q  e.g. %q", b.z, b.raw, b.vals, slot, tt.Template, sample)
	}
}
