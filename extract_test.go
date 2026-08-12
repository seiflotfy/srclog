package srclog

import "testing"

func TestExtract(t *testing.T) {
	m, err := Extract("testdata/sample")
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{} // template -> level
	for _, tpl := range m.Templates {
		got[tpl.Template] = tpl.Level
	}

	want := map[string]string{
		"dial <*>: <*>":                   "info",
		"starting server on <*>":          "info",
		"cannot open config":              "fatal",
		"user logged in":                  "info",
		"query failed":                    "error",
		"cache miss":                      "info",
		"upload failed after <*> retries": "error",
		"db unreachable":                  "error",
		"read failed":                     "error",
		"retrying in <*>s":                "debug",
		"slow request<*>":                 "warn", // fmt.Sprint: no space guaranteed after a string operand
		"boot <*>":                        "info",
	}
	for tmpl, level := range want {
		if got[tmpl] != level {
			t.Errorf("template %q: got level %q, want %q (missing?)", tmpl, got[tmpl], level)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d templates, want %d: %v", len(got), len(want), got)
	}
	// l.Error(err.Error()) + the zero-literal sugar.Errorf("%v", err)
	if m.Stats.Dynamic != 2 {
		t.Errorf("Dynamic = %d, want 2", m.Stats.Dynamic)
	}
	if m.Stats.ParseErrors != 0 {
		t.Errorf("ParseErrors = %d, want 0", m.Stats.ParseErrors)
	}
}

func TestNormalizeFormat(t *testing.T) {
	cases := map[string]string{
		"dial %s: %v":       "dial <*>: <*>",
		"done %d%%":         "done <*>%",
		"%+v / %#v / %-10s": "<*> / <*> / <*>",
		"%[1]d and %.2f":    "<*> and <*>",
		"width %*d":         "width <*>",
		"no verbs":          "no verbs",
	}
	for in, want := range cases {
		if got := normalizeFormat(in); got != want {
			t.Errorf("normalizeFormat(%q) = %q, want %q", in, got, want)
		}
	}
}
