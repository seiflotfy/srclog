package srclog

import (
	"path/filepath"
	"testing"
)

func dictMatcher(t *testing.T) *Matcher {
	t.Helper()
	merged := &Manifest{Version: 1, Anchor: "suffix"}
	paths, err := filepath.Glob("dicts/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no dictionaries found: %v", err)
	}
	for _, p := range paths {
		m, err := LoadManifest(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if m.Anchor != "suffix" {
			t.Fatalf("%s: anchor = %q, want suffix", p, m.Anchor)
		}
		merged.Templates = append(merged.Templates, m.Templates...)
	}
	dict, err := NewMatcher(merged)
	if err != nil {
		t.Fatal(err)
	}
	return dict
}

func TestSuffixMatching(t *testing.T) {
	dict := dictMatcher(t)

	cases := []struct {
		in string
		id string
	}{
		// mid-chain, after the ": " wrap boundary
		{`handler: load user 42: pq: duplicate key value violates unique constraint "users_email_key"`, "pg-duplicate-key"},
		// whole string
		{"context deadline exceeded", "ctx-deadline"},
		// specific beats framing: pg-duplicate-key over pg-any
		{`pq: duplicate key value violates unique constraint "x"`, "pg-duplicate-key"},
		// framing catches the rest
		{"pq: something novel", "pg-any"},
		{"read tcp 10.0.0.1:443: read: connection reset by peer", "net-conn-reset"},
		{"call thing: rpc error: code = NotFound desc = dataset gone", "grpc-status"},
	}
	for _, c := range cases {
		m, ok := dict.Match(c.in)
		if !ok {
			t.Errorf("Match(%q): no match, want %s", c.in, c.id)
			continue
		}
		if m.Template.ID != c.id {
			t.Errorf("Match(%q) = %s, want %s", c.in, m.Template.ID, c.id)
		}
	}

	// The ": " boundary must hold: no word-internal suffix matches.
	if m, ok := dict.Match("fooEOF"); ok {
		t.Errorf("Match(fooEOF) = %s, want no match", m.Template.ID)
	}
}

func TestResolveCascade(t *testing.T) {
	primary, err := NewMatcher(&Manifest{Templates: []*Template{
		{ID: "src-1", Template: "query users failed: <*>", Level: "error"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	dict := dictMatcher(t)

	// Source template match, param cascades into the pg dictionary.
	n, ok := Resolve(primary, dict, `query users failed: pq: duplicate key value violates unique constraint "users_email_key"`)
	if !ok || n.ID != "src-1" {
		t.Fatalf("Resolve top = %+v ok=%v, want src-1", n, ok)
	}
	child, isNode := n.Params[0].(*Node)
	if !isNode || child.ID != "pg-duplicate-key" {
		t.Fatalf("param = %#v, want nested pg-duplicate-key node", n.Params[0])
	}
	if len(child.Params) != 1 || child.Params[0] != "users_email_key" {
		t.Fatalf("nested params = %#v, want [users_email_key]", child.Params)
	}

	// Bare-printed error: no source template, dictionary catches the line.
	n, ok = Resolve(primary, dict, "pq: deadlock detected")
	if !ok || n.ID != "pg-deadlock" {
		t.Fatalf("Resolve bare = %+v ok=%v, want pg-deadlock", n, ok)
	}

	// grpc desc recurses a second level.
	n, ok = Resolve(primary, dict, "query users failed: rpc error: code = Unavailable desc = context deadline exceeded")
	if !ok {
		t.Fatal("Resolve grpc line failed")
	}
	g := n.Params[0].(*Node)
	if g.ID != "grpc-status" {
		t.Fatalf("param = %s, want grpc-status", g.ID)
	}
	if desc, isNode := g.Params[1].(*Node); !isNode || desc.ID != "ctx-deadline" {
		t.Fatalf("desc param = %#v, want nested ctx-deadline", g.Params[1])
	}

	// Sprint-style capture includes the joining space; the cascade must still
	// find the dictionary entry.
	sprintPrimary, err := NewMatcher(&Manifest{Templates: []*Template{
		{ID: "src-2", Template: "failed to acquire lock<*>", Level: "error"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	n, ok = Resolve(sprintPrimary, dict, "failed to acquire lock pq: deadlock detected")
	if !ok {
		t.Fatal("Resolve sprint line failed")
	}
	if child, isNode := n.Params[0].(*Node); !isNode || child.ID != "pg-deadlock" {
		t.Fatalf("sprint param = %#v, want nested pg-deadlock", n.Params[0])
	}

	// Without a dictionary, params stay plain strings.
	n, ok = Resolve(primary, nil, "query users failed: boom")
	if !ok || n.Params[0] != "boom" {
		t.Fatalf("Resolve without dict = %+v ok=%v, want string param", n, ok)
	}
	if _, ok = Resolve(primary, nil, "pq: deadlock detected"); ok {
		t.Fatal("Resolve without dict matched a bare error")
	}
}
