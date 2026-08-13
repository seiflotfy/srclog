package srclog

import "testing"

func TestExtractErrors(t *testing.T) {
	m, err := ExtractErrors("testdata/errsample")
	if err != nil {
		t.Fatal(err)
	}
	if m.Anchor != "suffix" {
		t.Fatalf("Anchor = %q, want suffix", m.Anchor)
	}

	got := map[string]string{} // template -> lib
	for _, tpl := range m.Templates {
		got[tpl.Template] = tpl.Lib
	}
	want := map[string]string{
		"load user <*>: <*>": "fmt",
		"user not found":     "errors",
		"open <*>: <*>":      "errors", // Wrapf appends ": <cause>"
		"no dataset <*>":     "status",
	}
	for tmpl, lib := range want {
		if got[tmpl] != lib {
			t.Errorf("template %q: got lib %q, want %q (missing?)", tmpl, got[tmpl], lib)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d templates, want %d: %v", len(got), len(want), got)
	}
	if m.Stats.Dynamic != 1 {
		t.Errorf("Dynamic = %d, want 1", m.Stats.Dynamic)
	}
}
