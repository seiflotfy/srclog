# Handover

State of srclog as of 2026-08-13, after two build days. Everything below is
committed, pushed, and CI-green; every number was measured on real corpora
with byte-exact round-trip verification. The reasoning behind each choice is
in `decisions.md` (54 numbered decisions) — read that before re-litigating a
design.

## What this is

Log templates are born as format strings in source. srclog extracts them at
merge time (exact, with file:line/level/commit), matches runtime lines back
to them at ingest, mines only the residual, and encodes messages as a
columnar template codec. One Go module, zero dependencies.

```
source (Go+Rust) ──extract──▶ manifests ──promote──▶ append-only catalog (aliases)
dependencies    ──errors───▶ suffix dictionaries          │
runtime residual ──mine────▶ candidates ──────────────────┘
                                                          ▼
log line ──match/Resolve──▶ nested Node (IDs+params) ──BuildColumn──▶ tc02 column
                                        │
                                        └──enums──▶ (template, slot, values) enrichment
```

## Component map

| file | what |
|---|---|
| `extract.go` | Go AST walker: stdlib log, slog, zap, logrus, zerolog; `recommended_dicts` from imports |
| `rust.go` | Rust tracing/log macro scanner (masking pass, field syntax, `event!(Level::X)`) |
| `errors.go` | `srclog errors`: harvest error-construction sites → suffix dictionaries |
| `template.go` | manifest model, placeholder normalization, line/suffix regex derivation |
| `match.go` + `litmatch.go` | Matcher: exact map + boundary-token-indexed literal-segment scan (regex only for the newline corner); lossless suffix prefixes; `MatchByID` for routers |
| `cascade.go` | `Resolve`: nested decomposition through dictionaries, depth-capped |
| `mine.go` | drain-style miner, bounded (64/bucket, 4096 total) |
| `catalog.go` | `Promote`: append-only catalog, subsumption gate, scanned-beats-mined aliases |
| `enums.go` | low-cardinality slot detection → embedding-ready (template, slot, values) |
| `column.go` | tc02 codec: dense codes, recursive sub-IDs, slot buckets + dicts, block-constant folding |
| `cmd/srclog` | CLI: extract, errors, match, mine, promote, enums |
| `dicts/` | curated service dictionaries; `dicts/gen/` generated from 8 real client libraries |
| `contrib/colbench` | corpus benchmark (round-trip verified) + shapegen (drain3 token shapes) |
| `action.yml`, `.github/workflows/ci.yml` | per-commit manifest artifact; vet+test+self-extract |

## The numbers (all zstd-9, byte-exact verified)

**Match rates** (staging `k8s-logs-staging`/`axiom-obs`, 50k msgs; prod
`k8s-logs-prod`/`cloud-prod`, 100k msgs — fetched via the staging API, creds
in `~/.axiom.toml`):

- Go manifest only: 67.9% → + Rust extractor: 94.7% → + mined catalog: **99.95% staging, 99.94% prod**
- Services whose code is scanned match ~100%; the residual is always attributable (framing, other codebases)
- Version skew is real: 14-month-stale metrics checkout scored 81% vs deployed-tag 94.7% — extract at the release tag from the image label

**Compression, message column vs stringv1 (`UnsafeByteSlice`)**:

| codec | prod (healthy) | incident (30% error burst) |
|---|---|---|
| stringv1 | 65.2 KB — 1.00x | 310.9 KB — 1.00x |
| drain3 column, best knobs (sim=0.7, full-train) | 31.8 KB — 2.05x | 163.9 KB — 1.90x |
| shapes (catalog→drain3 bridge, folded) | 29.7 KB — 2.19x | 164.6 KB — 1.89x |
| **tc02 + folding (this repo)** | **31.0 KB — 2.10x** | **153.6 KB — 2.02x (best measured)** |

**Speed** (rewritten 2026-08-13, decisions #55-56): literal-segment matching
replaced regexp on the hot path, suffix dicts got a trailing-token index, and
BuildColumn resolves through a memoizing `Resolver`. Same-box benchmarks
(synthetic zipf corpus, `bench_test.go`): Match 16x faster, Resolve 11x,
worst-case miss 170x, dict param hit 41x, BuildColumn 17.6x (~1.3M rows/s/core
— and with a Resolver reused across blocks (BuildColumnWith, decision #57), warm blocks encode at ~138ns/row, faster than drain3's column build). All pinned to the old
regex semantics by differential tests (`litmatch_test.go`). A drain3-routed
matcher was built and measured (`contrib/fastmatch`, separate module, root
stays zero-dep): coverage-identical, but it loses to the new core on
realistic manifests (162ns vs 408ns/line) and only wins pathological
first-token skew — kept as a measured escape hatch, not wired in.

**Size attribution** (`column_breakdown_test.go`): ~half the healthy-traffic
column is the rowID sequence (the log itself, ~1.3 bits/row — irreducible
without reordering); most of the rest is a handful of high-entropy param
buckets (IPs, UUIDs, durations). Structure costs ~nothing.

## Reviews

Three rounds of goreview robpike + bradfitz judge personas; final scores
9.5/9.5, 9.5/9, 9.5/8.5 — zero open deductions. One review claim was refuted
with evidence (nats.go module naming, decision #40). The tc02 codec has NOT
had a judge round yet — worth one before production use.

## Reproduce the measurements

```sh
# manifests (internal repos; keep artifacts out of this public repo)
srclog extract -o axiom.json ~/Code/axiom
git -C ~/Code/metrics archive <deployed-tag> | tar -x -C /tmp/m && srclog extract -o metrics.json /tmp/m
# corpora: query staging API (deployments in ~/.axiom.toml), project the log
# field, unwrap zap `msg` / tracing `message` locally — see decisions #45
# pipeline
srclog match -t merged.json -d dicts/stdlib.json ... corpus.txt
srclog mine -t merged.json -min 20 -o cand.json corpus.txt && srclog promote -catalog cat.json cand.json
srclog enums -t merged.json -d dicts/*.json corpus.txt
go run ./contrib/colbench merged.json corpus.txt dicts/*.json   # sizes + round-trip
# comparison vs stringv1/drain3: srclog_compare_test.go in the
# axiom-drain3-column checkout (pkg/kirby/column/strvec/), env-gated:
#   SRCLOG_CORPUS=... [SRCLOG_MANIFEST=... SRCLOG_SHAPES=...] go test -count=1 -run TestCompareColumnSizes -v
```

## Open threads, ranked

1. ~~**tc02 speed**: param memoization + boundary-token index for suffix dicts~~
   — DONE (decisions #55-56: 17.6x on BuildColumn; drain3 router measured and
   rejected as default, see `contrib/fastmatch`). Remaining speed lever if
   ever needed: re-measure on the real corpora via colbench.
2. **Typed leaf buckets**: ip:port/durations/timestamps as binary+delta — the
   incident corpus's single biggest bucket (53.7 KB of 153.6) is ip:port text.
3. **Signature dedup**: hoist repeated cascade subID chains into the table
   (~8% of incident column) — matches the kirby-thread TemplatedColumn design
   (flattened cascade signature as collection key, catalog_epoch header,
   0 = unmatched on wire).
4. **Kirby integration**: implement tc02 behind `strvec.StrVec` in the axiom
   repo (seam: `BuildColumn` where `drainCompress` sits; wire needs msgp
   framing + the type byte).
5. **Judge round on column.go/enums.go** (newest, least-reviewed code).
6. **Catalog service**: Promote exists as a CLI; productionizing means the
   append-only invariants from `docs/superpowers/specs/2026-08-13-srclog-catalog-design.md`
   plus distribution (manifest artifacts already flow from the Action).
7. **Embeddings**: embed enriched catalog templates + enum triples, compose
   row vectors over cascade signatures — never embed raw lines (the four
   promotion mechanisms are all implemented now).
8. **TypeScript extractor** via tree-sitter — same manifest seam.

## External artifacts

- Pitch deck (live, private): https://claude.ai/code/artifact/b50477a0-ea51-46c4-81bb-a5d11e2289c8
  — rebuilt 2026-08-13, numbers synced through decision #54 (tc02 +
  block-constant folding, enums, size attribution); slide 009 carries the
  compression story, appendix has demo script + murder board. Supersedes
  https://claude.ai/code/artifact/961f0253-0fce-4024-88c8-0762296932a6, which
  this account can no longer read or update (served as public non-member;
  not in the account's artifact list) — treat the old link as stale.
- `axiom-drain3-column` checkout: comparison harness added at
  `pkg/kirby/column/strvec/srclog_compare_test.go` (uncommitted there; the
  branch has unrelated WIP — don't clobber).
- Generated corpus/manifest artifacts live in the session scratchpad only —
  regenerate per the commands above; none belong in this public repo.
