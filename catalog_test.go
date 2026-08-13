package srclog

import "testing"

// A suffix manifest must never sit in the catalog seat — subsumption would
// run mid-string and mass-alias candidates into unrelated IDs.
func TestPromoteRejectsSuffixCatalog(t *testing.T) {
	_, err := Promote(&Manifest{Version: 1, Anchor: "suffix"}, &Manifest{})
	if err == nil {
		t.Fatal("Promote accepted a suffix-anchored catalog")
	}
}

func TestPromote(t *testing.T) {
	catalog := &Manifest{Version: 1, Templates: []*Template{
		{ID: "src-1", Template: "dial <*>: <*>", Level: "info", Lib: "log"},
	}}

	// Batch 1: one novel mined candidate, one subsumed by src-1, one duplicate ID.
	res, err := Promote(catalog, &Manifest{Templates: []*Template{
		{ID: "min-1", Template: "worker <*> crashed", Lib: "mined"},
		{ID: "min-2", Template: "dial <*>: refused", Lib: "mined"}, // covered by dial <*>: <*>
		{ID: "src-1", Template: "dial <*>: <*>", Lib: "log"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Admitted != 1 || res.Aliased != 1 || res.Skipped != 1 {
		t.Fatalf("res = %+v, want 1 admitted, 1 aliased, 1 skipped", res)
	}
	if catalog.Aliases["min-2"] != "src-1" {
		t.Fatalf("aliases = %v, want min-2 → src-1", catalog.Aliases)
	}
	if len(catalog.Templates) != 2 {
		t.Fatalf("catalog has %d templates, want 2", len(catalog.Templates))
	}

	// Batch 2: a source scan later produces exactly what min-1 approximated —
	// admit it and re-point the mined ID.
	res, err = Promote(catalog, &Manifest{Templates: []*Template{
		{ID: "src-2", Template: "worker <*> crashed", Level: "error", Lib: "log"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Admitted != 1 || res.Aliased != 1 {
		t.Fatalf("res = %+v, want 1 admitted, 1 aliased", res)
	}
	if catalog.Aliases["min-1"] != "src-2" {
		t.Fatalf("aliases = %v, want min-1 → src-2", catalog.Aliases)
	}

	// Append-only: nothing was removed, src-1 untouched.
	if len(catalog.Templates) != 3 || catalog.Templates[0].ID != "src-1" {
		t.Fatalf("catalog mutated: %+v", catalog.Templates)
	}

	// A matcher built from the catalog emits the canonical ID, not the
	// superseded mined one.
	matcher, err := NewMatcher(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := matcher.Match("worker 9 crashed"); !ok || got.Template.ID != "src-2" {
		t.Fatalf("post-supersession match = %+v ok=%v, want src-2", got, ok)
	}

	// Re-promoting an aliased candidate is a no-op skip.
	res, err = Promote(catalog, &Manifest{Templates: []*Template{
		{ID: "min-1", Template: "worker <*> crashed", Lib: "mined"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || res.Admitted != 0 {
		t.Fatalf("res = %+v, want pure skip", res)
	}
}
