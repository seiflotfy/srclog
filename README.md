# srclog

Extract log message templates from Go **source code** and match runtime log lines back
to them — template ID, level, source location, and extracted parameters included.

Your format strings already *are* the templates. No mining (drain3 etc.) required:

```go
log.Printf("dial %s: %v", host, err)   →   "dial <*>: <*>"  (info, server/conn.go:42)
```

## Usage

```sh
go install github.com/seiflotfy/srclog/cmd/srclog@latest

# in CI or locally: produce the manifest artifact
srclog extract -o srclog-templates.json .

# anywhere else: match log lines against it
tail -f app.log | srclog match -t srclog-templates.json
{"id":"7256352f80eb","level":"info","template":"dial <*>: <*>","params":["10.0.0.1:5432","connection refused"]}
```

Or as a library: `srclog.Extract(dir)` / `srclog.NewMatcher(manifest)` /
`srclog.Resolve(primary, dict, line)`.

### Cascade matching (service error dictionaries)

Params often *end in* a well-known service error (`handler: load user 42: pq: …`).
Suffix-anchored dictionaries decompose them — and catch bare-printed errors that have
no source template at all:

```sh
tail -f app.log | srclog match -t srclog-templates.json -d dicts/stdlib.json -d dicts/postgres.json
{"id":"a8ace617d749","level":"error","template":"failed to acquire lock<*>",
 "params":[{"id":"pg-deadlock","lib":"pq","template":"pq: deadlock detected"}]}
```

`dicts/` ships curated starters (stdlib, postgres, mysql, redis, grpc, kafka, nats,
aws) plus generated ones under `dicts/gen/` (pq, pgx, go-redis, mysql, grpc, sarama,
nats, mongo — harvested from the libraries' own source, version-stamped). Load the
services you use, not all of them. For anything else, your `go.mod` is the service
list:

```sh
go mod vendor && srclog errors -o deps-dict.json vendor/
```

### Mining the residual (catalog feedback loop)

What neither source scans nor dictionaries explain gets mined — match first, cluster
only the residual, then gate candidates into an append-only catalog with immutable IDs
and an alias table (mining proposes, the catalog disposes):

```sh
srclog mine -t srclog-templates.json -d dicts/stdlib.json -min 3 -o candidates.json app.log
srclog promote -catalog catalog.json candidates.json
```

Re-promotion is idempotent; when a later source scan produces the exact template a
mined entry approximated, the scanned entry is admitted and the mined ID aliased to it
— old references keep resolving, nothing re-encodes. Design: `docs/superpowers/specs/2026-08-13-srclog-catalog-design.md`.

### GitHub Action

```yaml
- uses: actions/checkout@v4
- uses: actions/setup-go@v5
  with: { go-version: stable }
- uses: seiflotfy/srclog@main
```

Uploads `srclog-templates-<sha>` as a workflow artifact for downstream matchers to
download.

## What it recognizes

stdlib `log`, `log/slog` (incl. `*Context` variants), zap (Logger + Sugared), logrus,
zerolog (`.Msg`/`.Msgf` chains) — plus anything else with `Infof`/`Error`/`Warnf`-shaped
methods (klog, glog, hclog fall out for free). Recognition is syntax-only by method
name: no type checking, no need to build the target repo.

Printf verbs normalize to `<*>`; string concatenation folds (`"boot "+version` →
`boot <*>`); fully dynamic messages are counted in `stats.dynamic`, not guessed.

## Manifest

```json
{
  "version": 1,
  "module": "github.com/acme/api",
  "commit": "abc123",
  "stats": {"files": 120, "calls": 340, "dynamic": 12, "parse_errors": 0},
  "templates": [{
    "id": "7256352f80eb",
    "template": "dial <*>: <*>",
    "level": "info",
    "lib": "log",
    "locations": [{"file": "server/conn.go", "line": 42}]
  }]
}
```

No regex is stored — matchers in any language derive it: QuoteMeta the literal parts,
join with one capture group per `<*>`, anchor `^...$` (or `(?:^|: )...$` when the
manifest says `"anchor": "suffix"`).

`id` is stable across commits (hash of level + template), so counts aggregated by ID
survive redeploys. Manifests are stamped with the commit — merge the manifests of the
releases you actually run when versions overlap in prod.

## Known limits (v1)

- Name-based recognition over-approximates: any type with an `Errorf` method counts as
  a logger. Extra templates are harmless — they just never match.
- `zap.Logger.Info(msg, fields...)` vs `logrus.Info(args...)` is disambiguated
  heuristically (`zap.X(...)` field args); zap fields built elsewhere may add trailing
  `<*>` placeholders.
- Matches the *message* portion of a line, not structured-handler output framing.
- Multiline templates are extracted verbatim but the line-oriented `match` command
  never matches them; lines over 1MiB are truncated and reported unmatched.
- Go only. Other languages are separate extractors emitting the same manifest.

See `decisions.md` for the reasoning behind each of these.
