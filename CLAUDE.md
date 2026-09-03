# CLAUDE.md

Guidance for Claude Code working in this repository.

## Always update the documentation

Any change to behaviour updates the docs that describe it, in the same commit.
That means all of these, not whichever one is nearest:

- the doc comment on the symbol, and on `Arena`/`Ref` if an invariant moved
- the runnable examples in [example_test.go](example_test.go) — they are
  compiled and diffed against their `// Output:` on every CI run, so a stale one
  fails the build rather than merely misleading someone
- [README.md](README.md), including the tables — they state exact numbers and
  exact semantics, both of which go stale silently
- this file, when the workflow or an invariant changes

Prose that contradicts the code is worse than no prose. Search before you assume
a claim lives in one place only: `grep -rn 'oversiz\|uniform\|standalone' *.go
README.md` is the kind of sweep that catches the copies.

## Pushing to `main` publishes a release

There is no manual release step, and no undo. A green CI run on `main` fires
[release.yml](.github/workflows/release.yml), which tags the exact SHA CI
validated, cuts a GitHub release, and asks the module proxy to fetch it so
pkg.go.dev indexes it. Patch by default; a marker anywhere in a commit message
in the push overrides:

| Marker | Bump |
| --- | --- |
| `#minor` | minor |
| `#major`, or a line starting `BREAKING CHANGE:` | major |
| `[skip release]` in the head commit | nothing is tagged |

Pre-1.0, an observable behaviour change takes `#minor` — that is what v0.2.0
was. Never tag by hand: the workflow reads the highest existing `vX.Y.Z` tag to
decide the next one, so a manual tag silently moves the series.

## Verifying a change

What CI runs, in order. All of it should pass locally before pushing:

```sh
go build ./...
go vet ./...                        # also checks Example function names resolve
go test -race -shuffle=on ./...     # -race matters: the package hands out unsafe views
go mod tidy -diff                   # must be a no-op
golangci-lint run                   # v2, version pinned in .github/workflows/ci.yml
go test -run='^$' -bench=. -benchtime=1x ./...
```

Coverage sits at 98.9% of statements, and the shortfall is exactly one line:
the `too many chunks` panic in `AppendRef`, which needs 2^31 chunks to reach —
51 GB of slice headers before any data. Keep it anyway; without it a chunk index
past 2^32 wraps small-positive and `Value` silently reads the wrong chunk. Every
other statement is covered, so treat a drop below that as a real gap rather than
noise.

`.golangci.yml` carries two deliberate gosec exclusions, G103 (unsafe) and G115
(the int32 narrowing in `AppendRef`). Both name a **file path**, so moving code
between files silently kills them. That has already happened once, when `b2s`
moved to `strings.go` and the `arena.go` rule went dead without failing
anything. After moving code, re-check them by running gosec with the exclusions
stripped and confirming the findings are the ones the rules claim to cover.

## Invariants that fail silently if broken

- **A chunk is never reallocated once a view points into it.** Views alias chunk
  storage directly, so growing a chunk in place would corrupt live data rather
  than error. A value that does not fit starts a new chunk.
- **An oversized chunk marks itself by its capacity.** `place` builds uniform
  chunks at exactly `chunkLen` and oversized ones at exactly the value's length,
  which is the whole basis for `isOversized`, which is what lets `Reset` drop
  them. Change how either kind is allocated and that equivalence breaks with
  nothing to catch it — tests assert the resulting `Retained` figures for both
  the copying path and `Reserve`.
- **`place` is the only placement policy, and the entry points are thin over
  it.** `Append`, `AppendRef` (and so `Intern`, `StrRef`) pass their value;
  `Reserve` passes nil and a count. Which chunk a value lands in is decided
  there and nowhere else — that is what keeps the copying and non-copying sides
  from drifting into two sets of rules. `place` hands each caller the region
  already in the shape it wants, which is not tidiness but budget: see below.
- **`Reserve` returns capacity exactly `n`, via a three-index slice.** Not the
  rest of the chunk: the exact cap is what makes overfilling reallocate instead
  of writing over the neighbouring value, so it is load-bearing rather than
  tidiness. `Test_unit_Arena_ReserveShape` asserts the cap and then overfills to
  prove it. Note `Append`'s view is NOT capped this way — it carries the chunk's
  slack, so appending to one corrupts the arena; that is what rule 3 ("never
  mutate what you were handed") covers, and it is why `Reserve` exists for
  callers who mean to fill a region themselves.

## Two compiler thresholds this package sits right on top of

Both were found the same way — a refactor that changed no logic at all and cost
20-27% — so check them after touching `place` or the entry points, with
`go build -gcflags=-m=2` and an interleaved A/B.

- **A `make` immediately followed by a `copy` that fills it skips the zeroing,
  and only when the two sit together.** Moving the copy to the caller cost
  `Append/65537` **+27%** — a whole extra pass over the value — and an isolated
  micro reproduces it exactly (3.40 → 4.33 µs on a 64 KiB make+copy). This is
  why `place` takes the source rather than returning a region for the caller to
  fill, and why its oversized branch writes `make([]T, len(src))` next to
  `copy(big, src)` with the length spelled the same way in both. `Reserve`'s arm
  of that branch deliberately keeps the zeroing: it has nothing to copy, and a
  zeroed region is what its doc promises for a chunk not yet reused.
- **`Append` inlines at cost 80 against a budget of 80, and `Reserve` at 79** —
  in the `go.shape` instantiation, which is the one that runs; the monomorphised
  `Arena[uint8]` figures are far lower and will mislead you. Base `Append` was
  79, and one extra argument took it to 81 and stopped it inlining, worth
  **+15%** on `Append/16`. That is the whole reason `place` shapes the returned
  region instead of the callers doing it: `place` costs 213 and never inlines,
  so work moved into it is free and work moved out of it is not.
- **`Ref` stays 12 bytes and pointer-free.** That is its entire reason to exist
  over the `[]T` from `Append`; a test asserts `unsafe.Sizeof`.
- **`Ref` addresses at most 2³¹−1 elements**, and `AppendRef` panics rather than
  let the conversion wrap. The guard is free *because* the uniform store path
  already caps `end` at the chunk's capacity — only an oversized value or an
  over-wide chunk can reach the bound — so do not move or "optimise" it without
  re-deriving that. `Test_unit_Ref_SizeLimits` pins the guard, the over-wide
  chunk case, and the fact that `Append` stays unbounded.

## Benchmarking honestly

The README quotes medians of five runs. Two things to respect:

- **The machine drifts.** Runs taken minutes apart have differed by ~7%, enough
  to invent or hide a regression. Compare **within one run** where possible: the
  `clone` arms of `BenchmarkIntern` never change, so they read as a baseline,
  and `BenchmarkAppendRef` runs both store paths side by side. A within-run
  comparison is how the one real regression in this repo was caught.
- **`BenchmarkIntern/arena/4096` swings by tens of percent** because a batch no
  longer fits the chunks and chunk allocation dominates. The README says so; do
  not quote it as a firm number.

Benchmarks that retain a batch must bound what they hold. An oversized value now
occupies a chunk until `Reset`, so `BenchmarkAppend` caps live bytes rather than
holding `batch` values — 4096 × 64 KiB would be a quarter of a gigabyte.
