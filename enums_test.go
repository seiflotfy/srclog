package srclog

import (
	"fmt"
	"testing"
)

func TestEnumTracker(t *testing.T) {
	primary, err := NewMatcher(&Manifest{Templates: []*Template{
		{ID: "src-query-fail", Template: "query users failed: <*>"},
		{ID: "src-ingested", Template: "Ingested <*> events"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	dict, err := NewMatcher(&Manifest{Anchor: "suffix", Templates: []*Template{
		{ID: "grpc-status", Template: "rpc error: code = <*> desc = <*>"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	tr := NewEnumTracker(8)
	codes := []string{"Unavailable", "Internal", "ResourceExhausted"}
	for i := 0; i < 300; i++ {
		// grpc slot 0 cycles 3 codes (enum); slot 1 and the count are unique (noise)
		line := fmt.Sprintf("query users failed: rpc error: code = %s desc = backend %d gone", codes[i%3], i)
		n, ok := Resolve(primary, dict, line)
		if !ok {
			t.Fatalf("line %d did not resolve", i)
		}
		tr.Observe(n)
		n, ok = Resolve(primary, dict, fmt.Sprintf("Ingested %d events", i))
		if !ok {
			t.Fatal("ingested line did not resolve")
		}
		tr.Observe(n)
	}

	set := tr.Enums(16)
	if len(set.Enums) != 1 {
		t.Fatalf("got %d enums, want 1: %+v", len(set.Enums), set.Enums)
	}
	e := set.Enums[0]
	if e.TemplateID != "grpc-status" || e.Slot != 0 || e.Seen != 300 {
		t.Fatalf("unexpected enum: %+v", e)
	}
	want := []string{"Internal", "ResourceExhausted", "Unavailable"}
	for i, v := range want {
		if e.Values[i] != v {
			t.Fatalf("values = %v, want %v", e.Values, want)
		}
	}
}
