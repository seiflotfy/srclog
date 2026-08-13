package srclog

import "testing"

func TestExtractRust(t *testing.T) {
	m, err := Extract("testdata/rustsample")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{} // template -> level
	for _, tpl := range m.Templates {
		got[tpl.Template] = tpl.Level
		if tpl.Lib != "tracing" {
			t.Errorf("template %q: lib = %q, want tracing", tpl.Template, tpl.Lib)
		}
	}
	want := map[string]string{
		"user logged in":           "info",
		"dial <*>: <*>":            "error",
		"slow request <*>":         "warn",
		"retrying in <*>s":         "debug",
		"connected to <*>":         "info", // target: "net" skipped
		"escaped {literal} braces": "trace",
		"multiline dial <*>: <*>":  "error",
		// field values (scope = "datapoint") must not be taken as the message
		"failed to validate, skipping.": "warn",
		"request took too long":         "warn", // event!(Level::WARN, ...)
	}
	for tmpl, level := range want {
		if got[tmpl] != level {
			t.Errorf("template %q: got level %q, want %q (missing?)", tmpl, got[tmpl], level)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d templates, want %d: %v", len(got), len(want), got)
	}
	// error!("{}", err) and info!(message_var); nothing from strings/comments.
	if m.Stats.Dynamic != 2 {
		t.Errorf("Dynamic = %d, want 2", m.Stats.Dynamic)
	}
}
