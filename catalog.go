package srclog

import "strings"

// Promotion gate: candidates (mined or scanned) are admitted into a catalog
// manifest under four invariants:
//
//  1. Append-only: existing entries are never edited or removed.
//  2. Mining proposes, the catalog disposes: candidates that duplicate or are
//     subsumed by existing entries don't get in.
//  3. Match first, mine the residual: enforced upstream (srclog mine only
//     sees unmatched lines), which is what keeps re-learned variants out.
//  4. Aliases, never re-encoding: when a source-scanned template supersedes a
//     mined approximation, the mined ID is aliased to the scanned ID; both
//     entries remain, old references keep resolving.

// PromoteResult reports what the gate did with a candidate batch.
type PromoteResult struct {
	Admitted int
	Aliased  int
	Skipped  int
}

// Promote merges candidates into catalog in place. The catalog must be a
// valid line-anchored manifest; it gains entries and aliases, never loses any.
func Promote(catalog, candidates *Manifest) (PromoteResult, error) {
	var res PromoteResult
	byID := make(map[string]*Template, len(catalog.Templates))
	for _, t := range catalog.Templates {
		if t != nil {
			byID[t.ID] = t
		}
	}
	if catalog.Aliases == nil {
		catalog.Aliases = map[string]string{}
	}

	// The subsumption matcher is rebuilt after admits so candidates later in
	// the batch are checked against ones admitted earlier in it.
	matcher, err := NewMatcher(catalog)
	if err != nil {
		return res, err
	}
	dirty := false

	for _, c := range candidates.Templates {
		if c == nil || isZeroLiteral(c.Template) {
			res.Skipped++
			continue
		}
		if _, known := byID[c.ID]; known {
			res.Skipped++
			continue
		}
		if _, known := catalog.Aliases[c.ID]; known {
			res.Skipped++
			continue
		}

		if dirty {
			if matcher, err = NewMatcher(catalog); err != nil {
				return res, err
			}
			dirty = false
		}
		existing := subsuming(matcher, c)
		switch {
		case existing == nil:
			// Genuinely novel: admit.
			cp := *c
			catalog.Templates = append(catalog.Templates, &cp)
			byID[cp.ID] = &cp
			res.Admitted++
			dirty = true
		case existing.Lib == "mined" && c.Lib != "mined":
			// A scan produced the exact template a mined entry approximated:
			// admit the scanned one, alias the mined ID to it (and re-point
			// any aliases that targeted the mined ID).
			cp := *c
			catalog.Templates = append(catalog.Templates, &cp)
			byID[cp.ID] = &cp
			catalog.Aliases[existing.ID] = cp.ID
			for old, canon := range catalog.Aliases {
				if canon == existing.ID {
					catalog.Aliases[old] = cp.ID
				}
			}
			res.Admitted++
			res.Aliased++
			dirty = true
		default:
			// Candidate is covered by an existing entry: alias, don't admit.
			catalog.Aliases[c.ID] = existing.ID
			res.Aliased++
		}
	}
	return res, nil
}

// subsuming returns a catalog entry that already covers the candidate, or
// nil. Coverage is checked pragmatically: the candidate's template, with its
// placeholders instantiated to a sentinel, must match the entry's pattern.
// (True regex-language containment is overkill for template shapes.)
func subsuming(m *Matcher, c *Template) *Template {
	const sentinel = "\x00srclog\x00"
	probe := strings.ReplaceAll(c.Template, Placeholder, sentinel)
	if got, ok := m.Match(probe); ok {
		return got.Template
	}
	return nil
}
