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
