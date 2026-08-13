package srclog

import (
	"bytes"
	"testing"
)

func TestColumnRoundTrip(t *testing.T) {
	primary, err := NewMatcher(&Manifest{Templates: []*Template{
		{ID: "src-query-fail", Template: "query users failed: <*>", Level: "error"},
		{ID: "src-boot", Template: "booted in <*>ms", Level: "info"},
		{ID: "src-ready", Template: "server ready", Level: "info"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	dict, err := NewMatcher(&Manifest{Anchor: "suffix", Templates: []*Template{
		{ID: "grpc-status", Template: "rpc error: code = <*> desc = <*>"},
		{ID: "net-conn-refused", Template: "dial tcp <*>: connect: connection refused"},
		{ID: "pg-deadlock", Template: "pq: deadlock detected"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	lines := []string{
		// 3-level nested: source → grpc → net
		"query users failed: rpc error: code = Unavailable desc = dial tcp 10.2.0.15:5432: connect: connection refused",
		"server ready",
		"booted in 843ms",
		// 2-level, exact sub-template with no leaf
		"query users failed: pq: deadlock detected",
		// unmatched raw
		"completely novel line 42",
		// plain leaf param
		"query users failed: mystery text",
		// repeats to exercise dedup and count
		"query users failed: rpc error: code = Internal desc = dial tcp db-7:5432: connect: connection refused",
		"server ready",
	}

	col := BuildColumn(lines, primary, dict)
	if col.Len() != len(lines) {
		t.Fatalf("Len = %d, want %d", col.Len(), len(lines))
	}

	var buf bytes.Buffer
	if _, err := col.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := ReadColumn(&buf)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := got.Rows()
	if err != nil {
		t.Fatal(err)
	}
	for i := range lines {
		if rows[i] != lines[i] {
			t.Errorf("row %d:\n got %q\nwant %q", i, rows[i], lines[i])
		}
	}

	// The table must carry catalog IDs for cross-block identity.
	seen := map[string]bool{}
	for _, e := range got.Templates() {
		seen[e.CatalogID] = true
	}
	for _, want := range []string{"src-query-fail", "grpc-status", "net-conn-refused", "pg-deadlock"} {
		if !seen[want] {
			t.Errorf("table missing %s: %+v", want, got.Templates())
		}
	}
}

func TestReadColumnRejectsGarbage(t *testing.T) {
	for _, data := range [][]byte{
		{}, []byte("nope"), []byte("tc01"),
		[]byte("tc01\xff\xff\xff\xff\xff\xff\xff\xff\xff\x01"), // absurd row count
	} {
		if _, err := ReadColumn(bytes.NewReader(data)); err == nil {
			t.Errorf("ReadColumn accepted %q", data)
		}
	}
}
