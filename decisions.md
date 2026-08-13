# Decisions

Running log of design/implementation decisions. Newest at the bottom.

## 2026-08-12

1. **Static extraction over log mining.** Templates come from Go source AST, not from
   runtime logs. Format strings *are* the templates — exact, with file/line/level/commit
   attached. Drain3/Tipping only ever made sense as a consumer-side fallback for
   unmatched lines; neither ships in v1.

2. **Drain3 over Tipping if/when a fallback miner is added.** The residual stream is
   low-volume, so Tipping's batch speed advantage is irrelevant there; Drain3's streaming
   API + state persistence is what that role needs. Revisit only if fallback volume
   becomes a batch-throughput problem.

3. **Go for both halves.** The likely consumer (Axiom ingest) is Go — matcher as a plain
   Go package means no FFI/WASM. `go/ast` is stdlib, so the Go extractor needs zero
   dependencies. TypeScript ruled out (wrong runtime for hot ingest path), Rust ruled out
   (cgo friction at a Go consumer).

4. **Ship data, not a per-repo binary.** The artifact is `srclog-templates.json`
   (versioned manifest, uploaded per commit by the GitHub Action). One shared matcher
   engine (`srclog` binary / importable package) loads any manifest. N repos × M commits
   of JSON is cheap; per-arch binaries per commit are not.

5. **Syntax-only extraction, no type checking.** Call sites are recognized by method
   name (`Infof`, `Error`, `Msg`, …), not by resolved receiver type. No
   `go/packages`, no dependency downloads in CI, works on any repo without building it.
   Cost: over-approximation (any type with an `Errorf` method is treated as a logger).
   Extra templates are harmless for matching; `fmt`/`http`/`errors`/`testing` receivers
   are explicitly excluded.

6. **Placeholder syntax is `<*>`** (drain-style convention). Printf verbs (`%s`, `%d`,
   `%+v`, `%[1]q`, …) normalize to `<*>`; `%%` normalizes to `%`. Templates without
   placeholders match by exact string lookup; the rest get an anchored regex with
   `(.*?)` capture groups.

7. **Template identity = sha256(level + template), first 12 hex chars.** Stable across
   commits and repos; call sites are attributes (a `locations` list), not identity.

8. **Supported libraries in v1:** stdlib `log`, `log/slog`, zap (Logger + Sugared),
   logrus, zerolog. klog/glog-style `Infof/Errorf/Warningf` fall out of the name-based
   approach for free.

9. **Dynamic messages are counted, not guessed.** A message argument that isn't (partly)
   a constant string increments `stats.dynamic` and produces no template. Partial
   constants (`"user " + name + " in"`) become templates with `<*>` at the dynamic parts.

10. **Matcher is exact-map + linear regex scan**, most-literal template first. Good
    enough for v1 template counts; token-trie is the known upgrade when it gets hot.

11. **Skipped in v1:** `_test.go` files, `vendor/`, `testdata/`, dot-dirs; multi-release
    version-skew handling (manifests are keyed by commit — consumers merge recent ones);
    non-Go languages (tree-sitter later, per-language extractors).

12. **Module path `github.com/seiflotfy/srclog`** — personal experiment repo; move to an
    org later is a rename, not a redesign.

## 2026-08-12/13 — field test + review round

Field test: the Axiom monorepo (5,762 files) extracts in ~1.5s → 3,923 templates,
9,910 log calls, 0 parse errors. ~44% of calls are dynamic (err.Error(), pre-built
strings) — that's the honest ceiling of static extraction there.

Review: goreview robpike judge scored 9/10, bradfitz judge 4/10. All deductions fixed:

13. **Zero-literal templates are dynamic.** `Errorf("%v", err)` produced template `<*>`
    whose regex matched *every* line — a catch-all that faked 100% match rates (found
    via the monorepo test, confirmed by review). Templates with no non-whitespace
    literal text now count as `stats.dynamic` and are not emitted.

14. **The Go matcher derives regexes from templates** and ignores the manifest's stored
    `regex` field (Pike: stored derived data with a silent sync invariant). The JSON
    field stays for non-Go consumers. Hand-built manifests no longer need it.

15. **`libGuess` is now genuinely metadata-only** (Pike: the "cosmetic" lib guess
    secretly flipped print→structured handling). The structured-fields decision is a
    direct predicate (`slog` receiver or all-`zap.X` args) at the one site that uses it.

16. **Manifest is validated at the boundary** (Fitzpatrick): `version != 1` and null
    template entries are bounded errors, not panics or silent mis-parses.

17. **`match` survives dirty streams** (Fitzpatrick): NDJSON encode errors abort with
    exit 1 (no more silent truncation on full disk), and a bounded reader truncates
    lines over 1MiB, emits them as unmatched, and keeps going — one pathological line
    no longer kills the stream. Truncated lines are never matched (anchored patterns
    against a prefix would lie).

18. **Extract writes tmp+rename** so a failed write can't destroy the previous manifest.

19. **`fmt.Sprint` vs `fmt.Sprintln` spacing** (Fitzpatrick): bare Print/logrus calls
    append `<*>` with *no* space (Sprint only spaces non-string operand pairs —
    statically unknowable, and `(.*?)` absorbs a leading space either way); `ln`
    variants keep the space. Dedup is also keyed by the raw level+template string now,
    not the truncated hash.

20. **Multiline templates (17 in the monorepo) are kept verbatim** even though the
    line-oriented matcher can never match them — they're true, and a record-oriented
    consumer could still use them.

## 2026-08-13 — review round 2 (final)

21. **`regexp.Compile` with a wrapped error, not `MustCompile`** in NewMatcher.
    regexFor output is always syntactically valid, but regexp's program-size budget
    (~128MB) can still reject a pathological manifest template — that must be a bounded
    error on the match path, not a panic (bradfitz re-review, round 2).

Final scores: robpike 9.5/10, bradfitz 9.5/10 — no open deductions. Accepted residual
ceilings, on record and deliberate: Extract aborts on unreadable directories (clean,
signaled), ParseFile reads are unbounded (conventional for Go tooling), no fsync before
the manifest rename (CI artifact, not a database), serialized `regex` JSON field has no
in-repo consumer (kept for non-Go matchers; delete in v2 if none materializes).

## 2026-08-13 — v2: service dictionaries + cascade matching

22. **A service dictionary is just a manifest** with `"anchor": "suffix"`. Same format,
    same Matcher, same NewMatcher — no second engine. Merged dictionaries (`-d` flags)
    become one suffix matcher.

23. **Suffix semantics: `(?:^|: )template$`.** Go's wrapping convention ("prefix: cause")
    means service errors are suffixes of params; the start-or-`": "` boundary lets
    `pq: <*>` fire mid-chain while keeping `EOF` from matching `fooEOF`. In suffix mode
    everything is a regex (exact-map shortcuts don't apply mid-string; dictionaries are
    small).

24. **Cascade via `Resolve(primary, dict, line)`**: params of a match — and whole lines
    that match no source template (the bare `log.Error(err)` case) — recurse through
    the dictionary, depth-capped at 4. Params trim leading space first (Sprint-style
    `msg<*>` templates capture the joining space — found in field test, would silently
    break the boundary). Output is a nested Node; a param is a string or a sub-Node.

25. **`go.mod` is the service list, not a curated registry.** New `srclog errors` mode
    harvests error-construction sites (fmt.Errorf, errors.New/Wrap/Wrapf/Errorf,
    status.Error/Errorf) into a suffix dictionary — point it at vendor/ or a module
    checkout (lib/pq@v1.10.9 → 77 real templates). Curation is only for messages born
    outside Go: `dicts/` ships ~60 stable entries (stdlib net/os/context/io/sql/json/
    tls/http + postgres, mysql, redis, grpc, kafka, nats, aws). Framing entries
    (`pq: <*>`, `ERR <*>`, `rpc error: … desc = <*>`) sort last by literal length, so
    specific entries always win; readable hand-assigned IDs (pg-deadlock) for curated
    entries, hashes for extracted ones.

26. **Deferred, deliberately:** the mining feedback loop (drain3 over accumulated
    unmatched params, promoted with `mined` provenance) and full error-chain
    decomposition via own-repo `fmt.Errorf` wrap prefixes — `srclog errors` on your own
    source already produces that dictionary if wanted.

## 2026-08-13 — v2 review round (robpike 8/10, bradfitz 7/10)

27. **The stored `regex` field is gone.** Both judges caught the same bug from opposite
    lenses: `srclog errors` stamped `anchor: suffix` on manifests whose per-template
    regexes were still `^...$` — the field documented "for non-Go consumers" lied to
    exactly them. Non-Go consumers derive patterns from template + anchor (documented
    in README); the duplicate representation class of bug is deleted, not patched.

28. **NewMatcher rejects zero-literal templates** (bradfitz): a hand-written
    `{"template": "<*>"}` dictionary entry compiled to a match-everything pattern and
    silently classified every unmatched line. The extractor-side guards are now
    defense-in-depth; the boundary every manifest crosses enforces it.

29. **Matcher knows its own anchor mode** (robpike): `Resolve` disables the cascade for
    a non-suffix dict instead of misclassifying with line semantics; loadDicts
    validates per file so errors name the bad dictionary. The unwritten `"line"`
    anchor alias was dropped.

Accepted note (unscored): the bare-line dictionary fallback drops the unmatched prefix
before the suffix match from its output — reconstruction needs the input line, which
the consumer still has. Add a prefix field if a consumer materializes that needs it.

Final v2 scores: robpike 9.5/10, bradfitz 9/10 — no open deductions. Both flagged the
same two stale comment fragments (fixed); bradfitz's residual half-point is the
standing legacy ceilings already on record above.

## 2026-08-13 — v3: pushed, generated dicts, miner, catalog

30. **Pushed to github.com/seiflotfy/srclog (public)** — the Action's
    `go run …@latest` path and CI now work for real.

31. **Generated dictionaries ship under `dicts/gen/`**: `srclog errors` over pq, pgx,
    go-redis, mysql, grpc, sarama, nats, mongo module-cache checkouts (~3.1k
    templates, version-stamped in `commit`). Load only the services a repo uses —
    merging everything makes the linear param scan pay for entries that can't match.

32. **Zero-literal bar raised to ≥2 alphanumeric literal chars.** Real-world catch:
    pgx's `Errorf("%s: %w")` → template `<*>: <*>` (literal `":"`) passed the old
    empty-check and suffix-matched nearly any line at top level. Punctuation-only
    literals carry no identity.

33. **Generated dictionary entries are labeled with the scanned module** (`pgx`), not
    the constructing package (`fmt`) — record's lib is overridden in ExtractErrors via
    shortModule when the scan root has a go.mod.

34. **Miner is drain-style, in Go, in-repo** (mine.go, ~100 lines): token-count +
    first-constant-token buckets, 0.5 similarity merge, `<*>` at disagreements,
    candidates gated by minSeen and the zero-literal bar, `lib: "mined"`. No drain3
    dependency — the algorithm is 100 lines, a Python sidecar isn't.

35. **The catalog is a role, not a type**: a plain Manifest with an `aliases` map.
    `Promote` implements the four invariants (append-only, gate, match-first-upstream,
    aliases-not-re-encoding) from the catalog design doc. Subsumption is pragmatic —
    sentinel-instantiate the candidate, match against the catalog — not regex-language
    containment. Supersession direction: scanned beats mined, alias chains re-pointed.

36. **`srclog mine` enforces match-first residual mining in the tool itself** — it
    takes the same -t/-d flags as match and clusters only what they miss.

## 2026-08-13 — v3 review round (robpike 8/10, bradfitz 6.5/10)

37. **Superseded catalog entries no longer compete for matches** (Pike): NewMatcher
    skips any template whose ID is an alias key. Before this, the older mined entry
    won fuzzy ties by insertion order and the matcher emitted deprecated IDs forever —
    the alias map existed in data but participated in no call path. runExtract's
    inline manifest writer was also folded into the shared writeJSON.

38. **Miner buckets are capped at 64 clusters** (Fitzpatrick): high-cardinality
    residual (unique-ID lines, hex noise) previously grew memory and O(n²) scan cost
    without bound — drain caps children per node, the port had dropped it. Overflow
    folds into the closest cluster (or drops when nothing is remotely similar);
    retained tokens are cloned so clusters don't pin large input lines.

39. **Promote rejects a non-line-anchored catalog** (Fitzpatrick): a suffix manifest
    in the catalog seat ran subsumption mid-string and mass-aliased candidates into
    unrelated dictionary IDs, then persisted the damage in the one artifact that is
    permanent by contract. Also: runPromote no longer treats stat failures as
    "catalog missing", and validates the catalog up front so errors carry its path.

40. **Pike's dead-code claim on shortModule's `.go` trim was rejected with evidence**:
    `github.com/nats-io/nats.go` is a scanned library; the trim is why its dictionary
    says `nats`. Review feedback gets verified, not blindly applied.

41. **Known tensions, accepted and documented**: (a) Promote's subsumption gate stops
    a more-specific scanned candidate from entering a catalog holding the generic
    shape — while NewMatcher's specificity sort exists to prefer specific entries;
    revisit if real catalogs hit it. (b) Curated dicts (slug IDs) and generated dicts
    (hash IDs) can cover the same error with two IDs; provenance differs, load what
    you need. (c) The stricter zero-literal bar means some manifests older binaries
    emitted now bounce under the same version: 1 — accepted pre-1.0, the catalog is
    the only long-lived artifact and it postdates the change.
