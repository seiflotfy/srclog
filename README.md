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

Or as a library: `srclog.Extract(dir)` / `srclog.NewMatcher(manifest)`.

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
    "regex": "^dial (.*?): (.*?)$",
    "level": "info",
    "lib": "log",
    "locations": [{"file": "server/conn.go", "line": 42}]
  }]
}
```

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
- Go only. Other languages are separate extractors emitting the same manifest.

See `decisions.md` for the reasoning behind each of these.
