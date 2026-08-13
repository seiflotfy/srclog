package srclog

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// End to end: extract the fixtures, match real-looking lines against them.
func TestExtractThenMatch(t *testing.T) {
	m, err := Extract("testdata/sample")
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := NewMatcher(m)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		line     string
		template string
		params   []string
	}{
		{"dial 10.0.0.1:5432: connection refused", "dial <*>: <*>", []string{"10.0.0.1:5432", "connection refused"}},
		{"cannot open config", "cannot open config", nil},
		{"upload failed after 3 retries", "upload failed after <*> retries", []string{"3"}},
		{"retrying in 5s", "retrying in <*>s", []string{"5"}},
		{"boot v1.2.3", "boot <*>", []string{"v1.2.3"}},
	}
	for _, c := range cases {
		got, ok := matcher.Match(c.line)
		if !ok {
			t.Errorf("Match(%q): no match, want %q", c.line, c.template)
			continue
		}
		if got.Template.Template != c.template {
			t.Errorf("Match(%q): got template %q, want %q", c.line, got.Template.Template, c.template)
		}
		if !reflect.DeepEqual(got.Params, c.params) {
			t.Errorf("Match(%q): got params %v, want %v", c.line, got.Params, c.params)
		}
	}

	if _, ok := matcher.Match("totally unknown line"); ok {
		t.Error("Match matched a line no template should cover")
	}
}

// More literal text wins over more placeholders.
func TestMatchSpecificity(t *testing.T) {
	manifest := &Manifest{Templates: []*Template{
		{ID: "a", Template: "dial <*>: <*>"},
		{ID: "b", Template: "dial <*>: timeout"},
	}}
	matcher, err := NewMatcher(manifest)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := matcher.Match("dial db:5432: timeout")
	if !ok || got.Template.ID != "b" {
		t.Fatalf("got %+v ok=%v, want template b", got, ok)
	}
	got, ok = matcher.Match("dial db:5432: connection refused")
	if !ok || got.Template.ID != "a" {
		t.Fatalf("got %+v ok=%v, want template a", got, ok)
	}
}

func TestNewMatcherRejectsNullTemplate(t *testing.T) {
	if _, err := NewMatcher(&Manifest{Templates: []*Template{nil}}); err == nil {
		t.Fatal("NewMatcher accepted a null template entry")
	}
}

// A template with no literal text would compile to a match-everything
// pattern; the boundary must reject it in both anchor modes.
func TestNewMatcherRejectsZeroLiteral(t *testing.T) {
	for _, anchor := range []string{"", "suffix"} {
		m := &Manifest{Anchor: anchor, Templates: []*Template{{ID: "x", Template: "<*>"}}}
		if _, err := NewMatcher(m); err == nil {
			t.Errorf("anchor %q: NewMatcher accepted a zero-literal template", anchor)
		}
	}
}

func TestLoadManifestVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.json")

	os.WriteFile(path, []byte(`{"version":2,"templates":[]}`), 0o644)
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("LoadManifest accepted an unsupported version")
	}

	os.WriteFile(path, []byte(`{"version":1,"templates":[]}`), 0o644)
	if _, err := LoadManifest(path); err != nil {
		t.Fatalf("LoadManifest rejected a v1 manifest: %v", err)
	}

	os.WriteFile(path, []byte(`{"version":1,"templates":[null]}`), 0o644)
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMatcher(m); err == nil {
		t.Fatal("NewMatcher accepted a manifest with a null template")
	}
}
