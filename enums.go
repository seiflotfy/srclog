package srclog

import "sort"

// Enum detection: a param slot whose values repeat heavily is an enum wearing
// a variable's clothes (grpc-status slot 0 = {Internal, ResourceExhausted,
// Unavailable} over 19k rows). Detected enums become catalog enrichment —
// embeddable identities — while high-cardinality slots (IDs, addresses) stay
// leaves. Promotion rule: a slot qualifies when tracking never saturated,
// it was seen enough to trust, and values repeat at least repeatFactor times
// on average.

// Enum is one detected (template, slot) value set.
type Enum struct {
	TemplateID string   `json:"template_id"`
	Slot       int      `json:"slot"`
	Values     []string `json:"values"`
	Seen       int      `json:"seen"`
}

// EnumSet is the serialized artifact, joinable to manifests by template ID.
type EnumSet struct {
	Version int    `json:"version"`
	Enums   []Enum `json:"enums"`
}

// EnumTracker accumulates per-(template, slot) value cardinality over
// resolved nodes. Memory is bounded: a slot stops tracking values once it
// exceeds maxDistinct (it is then disqualified — saturated means "not an
// enum", which is the common case and the cheap one).
type EnumTracker struct {
	maxDistinct int
	slots       map[slotKey]*slotStats
}

type slotKey struct {
	id   string
	slot int
}

type slotStats struct {
	seen      int
	values    map[string]struct{}
	saturated bool
}

// NewEnumTracker returns a tracker; maxDistinct <= 0 defaults to 64.
func NewEnumTracker(maxDistinct int) *EnumTracker {
	if maxDistinct <= 0 {
		maxDistinct = 64
	}
	return &EnumTracker{maxDistinct: maxDistinct, slots: map[slotKey]*slotStats{}}
}

// Observe walks a resolved node tree and records every string leaf under its
// (template, slot). Sub-nodes recurse — their slots are their own; the parent
// slot's identity is already carried by the cascade.
func (e *EnumTracker) Observe(n *Node) {
	for i, p := range n.Params {
		switch v := p.(type) {
		case *Node:
			e.Observe(v)
		case string:
			k := slotKey{n.ID, i}
			s := e.slots[k]
			if s == nil {
				s = &slotStats{values: map[string]struct{}{}}
				e.slots[k] = s
			}
			s.seen++
			if s.saturated || v == "" {
				continue // empty captures carry no meaning
			}
			s.values[v] = struct{}{}
			if len(s.values) > e.maxDistinct {
				s.saturated = true
				s.values = nil // free the tracking memory; disqualified
			}
		}
	}
}

// repeatFactor: values must repeat this many times on average before a slot
// counts as an enum — separates {Unavailable, Internal} from one-off text.
const repeatFactor = 4

// Enums returns the qualifying slots, values sorted, ordered by descending
// seen count. minSeen guards against slots too rare to judge.
func (e *EnumTracker) Enums(minSeen int) *EnumSet {
	set := &EnumSet{Version: 1}
	for k, s := range e.slots {
		// A single distinct value is a constant, not an enum — that belongs
		// to shape folding. Enums start at two.
		if s.saturated || s.seen < minSeen || len(s.values) < 2 || s.seen < repeatFactor*len(s.values) {
			continue
		}
		vals := make([]string, 0, len(s.values))
		for v := range s.values {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		set.Enums = append(set.Enums, Enum{TemplateID: k.id, Slot: k.slot, Values: vals, Seen: s.seen})
	}
	sort.Slice(set.Enums, func(i, j int) bool {
		if set.Enums[i].Seen != set.Enums[j].Seen {
			return set.Enums[i].Seen > set.Enums[j].Seen
		}
		if set.Enums[i].TemplateID != set.Enums[j].TemplateID {
			return set.Enums[i].TemplateID < set.Enums[j].TemplateID
		}
		return set.Enums[i].Slot < set.Enums[j].Slot
	})
	return set
}
