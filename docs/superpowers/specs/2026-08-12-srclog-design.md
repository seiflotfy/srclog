# srclog — source-derived log template extraction & matching

## Problem

Runtime log lines are unstructured strings, but almost every one of them was born as a
constant format string in source. Mining templates back out of log output (drain3 et al.)
is lossy and blind to provenance. Scanning the source instead yields *exact* templates
with level, file, line, and commit attached — and a matcher built from them can tag
runtime lines with a stable template ID and extracted parameters.

## Shape

Two halves, decoupled by a JSON manifest:

```
repo ──(GitHub Action: srclog extract)──▶ srclog-templates.json ──▶ artifact store
                                                                      │
log line ──▶ srclog match / srclog.Matcher ◀──────── download ────────┘
             └─▶ {template_id, level, template, params[]} | unmatched
```

## Components

- **`srclog` package** (root): manifest model, extractor, matcher.
  - `Extract(dir) (*Manifest, error)` — walks `*.go` (skips `_test.go`, `vendor/`,
    `testdata/`, dot-dirs), parses with `go/parser` (syntax only, no type checking),
    recognizes log call sites by method name, produces deduplicated templates.
  - `NewMatcher(*Manifest)` / `Match(line) (*Match, bool)` — exact-string map for
    placeholder-free templates, anchored-regex linear scan (most-literal first) for the
    rest. Returns template + captured params.
- **`cmd/srclog`**: CLI with `extract` and `match` subcommands. Zero dependencies.
- **`action.yml`**: composite GitHub Action — run extract, upload manifest as artifact
  keyed by `${{ github.sha }}`.
- **`.github/workflows/ci.yml`**: vet + test + self-extract on push.

## Extraction rules

Recognized method names decompose as `<Base><suffix>` where Base ∈ {Trace, Debug, Info,
Warn, Warning, Error, Fatal, Panic, DPanic, Print} and suffix ∈ {"", f, ln, w, Context}:

| shape | message handling | example |
|---|---|---|
| `f` suffix / `Msgf` | printf: verbs → `<*>` | `sugar.Errorf("dial %s: %v", …)` |
| `w` suffix | constant msg, args are fields | `sugar.Infow("cache miss", …)` |
| `Context` suffix | msg is arg 1, not 0 | `slog.ErrorContext(ctx, "q failed")` |
| bare / `ln`, receiver `slog` or all-`zap.X` args | constant msg, args are fields | `zl.Error("down", zap.Error(err))` |
| bare / `ln`, otherwise | print: `<*>` appended per extra arg | `log.Println("on", addr)` |
| zerolog `Msg`/`Msgf` | level recovered by walking the chain | `log.Error().Str(…).Msg("read failed")` |

Messages may be string literals or `+`-concatenations; non-constant parts become `<*>`;
fully dynamic messages are counted in `stats.dynamic` and skipped. Receivers
`fmt`, `http`, `errors`, `testing` are excluded (Errorf ≠ logging there).

## Manifest format (v1)

```json
{
  "version": 1,
  "module": "github.com/acme/api",
  "commit": "abc123…",
  "stats": {"files": 120, "calls": 340, "dynamic": 12, "parse_errors": 0},
  "templates": [
    {
      "id": "9f8e7d6c5b4a",
      "template": "dial <*>: <*>",
      "regex": "^dial (.*?): (.*?)$",
      "level": "info",
      "lib": "log",
      "locations": [{"file": "server/conn.go", "line": 42}]
    }
  ]
}
```

`id` = first 12 hex of sha256(level + "\x00" + template). `regex` omitted for exact
templates. Locations are attributes, not identity.

## Testing

`testdata/sample/` holds a non-compiled Go file exercising every supported library and
edge (concat, dynamic, excluded receivers). `extract_test.go` asserts the extracted
template set; `match_test.go` runs extract→match end-to-end plus specificity ordering.

## Non-goals (v1)

Non-Go languages, runtime mining fallback (drain3), version-skew merging at the
consumer, matching structured-handler *output* formats (the matcher targets the message
portion of a line).
