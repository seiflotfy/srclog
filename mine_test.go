package srclog

import "testing"

func TestMiner(t *testing.T) {
	mn := NewMiner()
	for _, line := range []string{
		"dial db-1.internal failed after 3 attempts",
		"dial db-2.internal failed after 7 attempts",
		"dial cache.internal failed after 2 attempts",
		"worker 12 started",
		"worker 40 started",
		"worker 7 started",
		"one-off line nobody repeats",
	} {
		mn.Add(line)
	}
	m := mn.Candidates(3)

	got := map[string]bool{}
	for _, tpl := range m.Templates {
		got[tpl.Template] = true
		if tpl.Lib != "mined" {
			t.Errorf("template %q: lib = %q, want mined", tpl.Template, tpl.Lib)
		}
	}
	for _, want := range []string{
		"dial <*> failed after <*> attempts",
		"worker <*> started",
	} {
		if !got[want] {
			t.Errorf("missing candidate %q in %v", want, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("got %d candidates, want 2: %v", len(got), got)
	}
	if m.Stats.Calls != 7 {
		t.Errorf("Stats.Calls = %d, want 7", m.Stats.Calls)
	}
}

// A variable first token must not shatter the cluster.
func TestMinerVariableFirstToken(t *testing.T) {
	mn := NewMiner()
	for _, line := range []string{
		"10.0.0.1 refused connection",
		"10.0.0.2 refused connection",
		"10.9.8.7 refused connection",
	} {
		mn.Add(line)
	}
	m := mn.Candidates(3)
	if len(m.Templates) != 1 || m.Templates[0].Template != "<*> refused connection" {
		t.Fatalf("got %+v, want one '<*> refused connection' candidate", m.Templates)
	}
}
