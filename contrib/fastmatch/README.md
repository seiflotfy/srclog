# fastmatch — drain3-routed matching (measured: usually not worth it)

An experiment answering "can we use [axiomhq/drain3](https://github.com/axiomhq/drain3)
to speed up matching?" — kept because the answer is a measured *it depends,
usually no*, and the numbers are worth preserving.

The Router over-approximates srclog templates into drain3 token shapes
(any token containing `<*>` becomes a wildcard position), lets drain3's
tokenizer + prefix tree propose a shape, verifies candidates exactly with
`srclog.MatchByID`, and falls back to the full srclog scan whenever the
fast path finds nothing. Coverage is identical to `srclog.Matcher.Match`
by construction (verified in `TestRouterCoverage`: 0 divergence, all
matches lossless, 20k lines).

## Measured (Xeon 2.80GHz, zstd-free pure matching, `-benchtime 2s`)

| regime | full scan (srclog) | router (drain3 + verify) |
|---|---|---|
| realistic zipf corpus, 300 templates | **162 ns/line** | 408 ns/line |
| adversarial: 500 templates sharing one first token | 2,345 ns/line | **438 ns/line** |

After srclog's literal-segment matcher and boundary-token indexes landed
(the same commit series that added this module), the core matcher is faster
than the drain3 round-trip on realistic traffic — the router's tokenization
pass is pure overhead when the first-token bucket holds a handful of
candidates that fail in ~15ns each. The router wins only when a manifest
has pathological token skew: hundreds of templates behind one first token.
Real corpora (prod: 129 templates carrying 100k events) look like the
first row.

**Default remains the zero-dependency core matcher.** Reach for this module
only if your manifest's largest first-token bucket has grown into the
hundreds — and re-measure before believing anything.

This module is intentionally separate so the root srclog module stays
dependency-free.
