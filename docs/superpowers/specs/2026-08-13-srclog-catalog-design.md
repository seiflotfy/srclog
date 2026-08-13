# srclog v3 — mining residual, promotion gate, append-only catalog

## Problem

Static extraction can't see dynamic messages (~44% of call sites in real repos) or
services outside the dependency graph. Mining covers that residual — but naive mining
re-learns templates every run, IDs churn, and six months of dashboards fracture.

## The four invariants

1. **Append-only catalog, immutable IDs.** A template, once admitted, is never edited,
   re-learned, or deleted; catalog versions only add entries. ID `abc123` means the
   same text forever, so cross-block group-bys can't fracture. "Deletion" = no longer
   used for new encoding; old references keep resolving.
2. **Mining proposes, the catalog disposes.** The miner never mints catalog entries
   directly. It emits candidates; the promotion gate discards duplicates, aliases
   subsumed shapes, and admits only the genuinely novel.
3. **Match first, mine only the residual.** `srclog mine` matches against the existing
   manifests/dictionaries and clusters only unmatched lines — the miner cannot
   re-learn a variant of something the catalog already explains. (Root-cause fix for
   week-to-week drift.)
4. **Aliases, never re-encoding.** When a source scan later produces the exact template
   a mined entry approximated, the scanned entry is admitted and the mined ID aliased
   to it (`aliases: {mined-id: scanned-id}`). Old references resolve through the alias
   table; nothing is rewritten.

## Implementation

- **`Miner`** (mine.go): drain-style — bucket by token count + first constant token,
  merge into the most similar cluster at ≥0.5 token agreement, disagreeing positions
  become `<*>`. `Candidates(minSeen)` emits clusters seen ≥ N times, `lib: "mined"`,
  zero-literal clusters dropped.
- **`Promote(catalog, candidates)`** (catalog.go): dedupe by ID and alias table;
  subsumption checked by instantiating the candidate's placeholders with a sentinel
  and matching against the catalog; mined→scanned supersession admits + aliases +
  re-points alias chains. The catalog is a plain `Manifest` with an `aliases` map —
  "catalog" is a role, not a type.
- **CLI**: `srclog mine [-t manifest] [-d dict ...] [-min 3] [-o candidates.json]`,
  `srclog promote -catalog catalog.json candidates.json ...` (created if missing,
  written tmp+rename).

## Consumer contract

Blocks/streams encode against a catalog version; the query layer resolves aliases at
group-by time. Worst-case gate mistake = two IDs for one pattern until an alias lands —
a dashboard annoyance, not corruption.

## Non-goals

Cross-repo catalog distribution and ownership (an ops question, not an engine one);
statistical drift detection; deleting entries (violates invariant 1 by design).
