# colbench + shapegen

Session tools promoted to the repo so measurements stay reproducible.

- `main.go` — builds a tc02 Column over a corpus (one log message per line),
  serializes, decodes, verifies byte-exact round-trip, reports sizes/timings:
  `go run . manifest.json corpus.txt [dict.json ...]`
- `shapegen.go` — catalog-matches a corpus and emits drain3-compatible token
  shapes with constant folding (see decisions.md #46-49 in the repo root and
  the axiom-side comparison harness):
  `go run -tags shapegen shapegen.go manifest.json corpus.txt shapes.json`

Nested module: kept out of the main library build on purpose.
