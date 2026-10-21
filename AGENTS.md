# Repository guidance

## Documentation

Update every affected description in the same commit as a behavior change:

- Symbol comments, including `Arena` and `Ref` when their invariants change.
- Runnable examples in [example_test.go](example_test.go).
- [README.md](README.md), including its API and benchmark tables.
- [SECURITY.md](SECURITY.md), whose "What counts" section distinguishes
  vulnerabilities from documented behavior.
- This file when an invariant or workflow changes.

Search for repeated claims before editing. For example:

```sh
rg -n 'oversiz|uniform|standalone' --glob '*.go' README.md SECURITY.md
```

Keep the project description consistent across the README, the package comment
in `arena.go`, the abstract in [CITATION.cff](CITATION.cff), and GitHub's repository
description (`gh repo edit --description`). Do not add a version or release date
to `CITATION.cff`; releases are automatic.

The README quick start must match the package-level `Example` and its output.
Keep that function name: the browser link targets pkg.go.dev's `#example-package`
anchor. Examples that keep data after a batch must clone it before `Reset` or
`Release` and use only the clone afterwards.

Every `.go` file starts with `// SPDX-License-Identifier: MIT` and a blank line.
The blank line keeps the license identifier out of the package documentation.

## Validation

Run `make check` before pushing. It runs these checks sequentially:

```sh
go build ./...
go vet ./...
go test -race -shuffle=on -count=1 ./...
go mod tidy -diff
golangci-lint run
go test -run='^$' -bench=. -benchtime=1x ./...
```

Make is optional; the commands work directly. Keep `Makefile` and
[CONTRIBUTING.md](CONTRIBUTING.md) aligned with
[CI](.github/workflows/ci.yml), including the pinned linter version.
`make help` lists formatting, coverage, fuzzing, and benchmark commands.

Upgrade the lint job's Go minor version and golangci-lint as a compatible pair;
the test matrix tracks stable independently. Validate with the official release
binary used by CI and the lint job's Go version. Rebuilding the same linter tag
with a different Go compiler can hide export-data incompatibilities.

Run tests with the race detector because the package returns unsafe views.
Examples must include `// Output:` so tests check their results. `go vet`
also checks that example names resolve to real symbols.

Coverage is 99.0%. The only uncovered statement is the `too many chunks` panic
in `AppendRef`, which requires more than 2³¹ chunks to reach. Keep the guard:
integer wraparound could otherwise resolve a reference to the wrong chunk.
Investigate any coverage drop.

`FuzzArena` mixes `Append`, `AppendRef`, `Reserve`, `Reset`, and `Release`,
checking live values and `Size`. Its seeds run with ordinary tests. Use
`make fuzz` for a 20-second randomized run.

The gosec exclusions in `.golangci.yml` are tied to file paths: G103 covers
unsafe conversions in `strings.go`, and G115 covers guarded int32 conversions
in `arena.go`. After moving that code, run gosec with the exclusions removed
and verify that the rules still match the intended findings.

## Storage invariants

- **Use an arena from one goroutine at a time.** `Arena` and `StringArena`
  require caller-provided synchronization if shared.
- **Never reallocate a chunk with live views.** A value that does not fit starts
  a new chunk. Views alias storage directly.
- **Never copy an arena after first use.** Copies share chunks but track placement
  independently. A nonempty `Ref` belongs to its originating arena and batch;
  it expires at `Reset` or `Release`, just like a view.
- **Count only successful placements in `Size`.** Include unfilled reservations.
  Increment after allocation succeeds. `AppendRef` checks descriptor limits
  after placement, so that panic still leaves the stored value counted.
- **`Reset` retains uniform chunk contents, including pointers.** Those pointers
  keep targets alive until overwritten or released. `Retained` counts chunk
  capacity, excluding pointees and bookkeeping overhead.
- **Capacity identifies oversized chunks.** `place` allocates uniform chunks at
  exactly `chunkLen` and oversized chunks at exactly the value's length.
  `isOversized` relies on that distinction so `Reset` can drop oversized chunks.
  Preserve the `Retained` assertions for both copying and reservation paths.
- **Keep placement policy in `place`.** `Append` and `AppendRef` pass a source;
  `Reserve` passes nil and a count. `AppendString` and `AppendStringRef` use
  those same paths.
  `place` returns the region in the shape each caller needs.
- **Cap reservations at exactly `n`.** Use a three-index slice with length zero
  so overfilling reallocates instead of overwriting neighboring values.
  `Test_unit_Arena_ReserveShape` checks both the capacity and overfilling.
  `Reserve` returns writable storage and does not clear reused contents.
  Clear the full region with `clear(region[:cap(region)])` when needed.
- **Copying-entry-point views and `Value` results are read-only.** Their capacity
  can extend into neighboring values. Do not modify or append to them.
- **Keep `Ref` 12 bytes and pointer-free.** Its int32 fields limit the stored
  range and chunk index to 2³¹−1. `AppendRef` must panic rather than truncate
  those values. `Test_unit_Ref_SizeLimits` checks the range guard, over-wide
  chunks, and the absence of that limit on `Append`.

## Performance-sensitive code

After changing `place` or its entry points, inspect compiler output with
`go build -gcflags=-m=2` and compare interleaved baseline and changed benchmarks.

- Keep the oversized copy branch's `make([]T, len(src))` immediately beside
  `copy(big, src)`. The compiler can skip zeroing a new allocation when it sees
  the copy fill it. Moving the copy to a caller can lose that optimization.
- Preserve zeroing for a new oversized reservation: it has no source to copy,
  and newly allocated storage must read as zero.
- Keep `Append` and `Reserve` small enough to inline. Inspect the `go.shape`
  instantiations that execute, rather than relying on concrete-type wrappers.
  Keep region shaping in `place` to protect the entry points' inline budget.
- Keep the reference bounds checks after placement. The uniform path already
  limits the end offset to chunk capacity; oversized values and over-wide
  chunks still need the explicit guard.

## Benchmarks

The README reports medians of five runs. Include the CPU, OS, Go version, and
commands when updating measurements.

- Alternate baseline and changed runs on the same machine to reduce drift.
  Use unchanged clone arms in `BenchmarkAppendString` as a within-run baseline.
  `BenchmarkAppendRef` compares both storage paths in one run.
- Treat `BenchmarkAppendString/arena/4096` as variable. Its 16 MiB batch spans 256
  default chunks; only the first batch allocates them, and later batches reuse
  them. Preserve that qualification when reporting results.
- Bound live bytes in benchmarks that retain batches. Oversized values occupy
  chunks until `Reset`, so a fixed value count can retain excessive memory.
- CI's single-iteration benchmark run checks that benchmarks execute. It is
  not a performance measurement.

## Community files

Keep support links and form names consistent across [SUPPORT.md](SUPPORT.md),
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), `.github/ISSUE_TEMPLATE/`, and
`.github/pull_request_template.md`. Questions use issues; link to Discussions
only if it is enabled. Security reports go to the private advisory form.

## Releases

A successful push or manually dispatched CI run on this repository's `main`
triggers [release.yml](.github/workflows/release.yml). It tags the exact SHA
that passed CI, creates a GitHub release, and requests module-proxy indexing.
Pull request CI runs cannot publish.

The default bump is patch. Commit-message markers in the push override it:

| Marker | Result |
| --- | --- |
| `#minor` | Minor bump |
| `#major`, or a line starting `BREAKING CHANGE:` | Major bump |
| `[skip release]` in the head commit | No release |

Pre-1.0, observable behavior changes require `#minor`. Never tag by hand: the
workflow uses the highest existing `vX.Y.Z` tag to choose the next version.
To request a release manually, dispatch **CI** on `main`, not Release.

The workflow skips commits already tagged, removed from `main`, or not
descending from the latest release. Preserve these checks so out-of-order CI
completion cannot publish older code under a newer version.
