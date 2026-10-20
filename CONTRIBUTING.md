# Contributing

Documentation fixes, questions, reproducible bugs, examples, and performance
investigations are all useful contributions. Small fixes can go straight to a
pull request. For a new API or a substantial behavior change, open an issue
first so we can discuss the use case and tradeoffs.

Please follow the [code of conduct](CODE_OF_CONDUCT.md). For help using the
library, see [SUPPORT.md](SUPPORT.md). Report suspected vulnerabilities privately
as described in [SECURITY.md](SECURITY.md).

## Get started

You need Git and Go 1.24 or newer. Fork the repository on GitHub if you want to
submit a pull request, then clone your fork. To explore the upstream checkout:

```sh
git clone https://github.com/JohanLindvall/arena.git
cd arena
git switch -c your-change
go test ./...
```

The first test run downloads the test dependencies. The library itself imports
only the standard library. No generated files or external services are needed.

For linting, install the binary version pinned in
[CI](.github/workflows/ci.yml). On Linux, macOS, or Git Bash on Windows:

```sh
curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b "$(go env GOPATH)/bin" v2.15.0
golangci-lint version
```

This installs into `$(go env GOPATH)/bin`; add that directory to your `PATH`.
The command follows the
[golangci-lint installation guide](https://golangci-lint.run/docs/welcome/install/local/)
and uses its recommended prebuilt binaries.

CI pairs this release with Go 1.27.x for linting. Use that Go minor version
and the official linter binary when reproducing CI; check both `go version`
and `golangci-lint version`. Rebuilding the same linter tag with a different
Go compiler can change its compatibility. The test matrix still covers the
minimum Go version, oldstable, and stable.

## Find your way around

| File | What it contains |
| --- | --- |
| [arena.go](arena.go) | Generic storage, placement, references, and batch lifetime |
| [strings.go](strings.go) | String entry points and the unsafe view conversions |
| [example_test.go](example_test.go) | Runnable examples published on pkg.go.dev |
| [arena_test.go](arena_test.go) | Unit tests, limits, and storage invariants |
| [arena_fuzz_test.go](arena_fuzz_test.go) | Mixed-operation fuzzing across batch boundaries |
| [arena_bench_test.go](arena_bench_test.go) | Allocation, throughput, and GC benchmarks |
| [AGENTS.md](AGENTS.md) | Repository guidance on invariants, performance, and releases |

Good first contributions include clarifying an example, reducing a reported
problem to a small test, or documenting a workload and its benchmark results.
Browse [open issues](https://github.com/JohanLindvall/arena/issues) and
[issues labeled good first issue](https://github.com/JohanLindvall/arena/issues?q=is%3Aissue%20is%3Aopen%20label%3A%22good%20first%20issue%22)
when available. Comment on a larger issue before starting to avoid duplicate work.

## Running the checks

With Make installed, `make check` runs the local equivalent of CI. `make help`
lists the other commands:

| Command | Purpose |
| --- | --- |
| `make test` | Tests, examples, and fuzz seeds with the race detector |
| `make fmt` | Apply gofmt and goimports using the linter configuration |
| `make check` | Build, vet, race tests, tidy check, lint, and benchmark smoke test |
| `make coverage` | Write `coverage.out` and print statement coverage |
| `make fuzz` | Run `FuzzArena` for 20 seconds |
| `make bench` | Run benchmarks five times with allocation counts |

Make is optional. These commands work directly, including on Windows:

```sh
go build ./...
go vet ./...
go test -race -shuffle=on -count=1 ./... # -race matters: the package hands out unsafe views
go mod tidy -diff                   # must be a no-op
golangci-lint run                   # v2, config in .golangci.yml
go test -run='^$' -bench=. -benchtime=1x ./...
```

The race detector needs CGO and, outside macOS, a C compiler; see
[Go's supported platforms and requirements](https://go.dev/doc/articles/race_detector#Requirements).
CI also checks coverage and tests the minimum Go version, oldstable, and stable
on Linux, macOS, and Windows. A local run checks your installed toolchain.

## Before opening a pull request

Keep each pull request focused on one problem. Describe the previous behavior,
what changes for users, and how you checked it. Include an issue link if there
is one; an issue is not required for a small fix.

- Add a test for new behavior or a regression. Keep examples runnable with
  `// Output:` so CI checks their output.
- Update all affected documentation together: symbol comments, examples,
  README tables, and the behavior described in `SECURITY.md`. Update
  `AGENTS.md` when an invariant or workflow changes.
- Run the checks above. If a check cannot run on your platform, say which one
  and why in the pull request.

A new `.go` file starts with `// SPDX-License-Identifier: MIT`, followed by a
blank line so it stays out of the package doc comment.

Keep the package-level `Example` function: the README's browser link targets
it. When changing the quick start, update both copies and verify the output.

## Performance changes

Measure on the same machine with the same Go version, and alternate baseline
and changed runs to reduce clock and temperature drift:

```sh
go test -run='^$' -bench=. -benchmem -count=5 ./...
```

Include the CPU, OS, Go version, commands, and results in the pull request.
Compare the unchanged clone arms of `BenchmarkIntern` within each run;
`BenchmarkIntern/arena/4096` is particularly variable. The CI benchmark step
runs one iteration to catch broken benchmarks, not to measure performance.

Read the [performance notes](AGENTS.md#performance-sensitive-code) before
changing `place` or its callers. A refactor can affect inlining and allocation
zeroing without changing behavior.

## Releases are automatic

Successful CI runs for pushes to `main` in this repository trigger
[release.yml](.github/workflows/release.yml). The workflow tags the exact commit
CI validated, skipping commits already tagged, no longer on `main`, or older
than/divergent from the latest release. Pull request runs cannot publish.
To trigger it manually, dispatch **CI** on `main`; release always waits for those
checks to pass. Patch by default. To ask for something else, put a marker anywhere
in a commit message in the push:

| Marker | Bump |
| --- | --- |
| `#minor` | minor |
| `#major`, or a line starting `BREAKING CHANGE:` | major |
| `[skip release]` in the head commit | nothing is tagged |

Don't tag by hand — the workflow reads the highest existing `vX.Y.Z` tag to
decide the next one, so a manual tag silently moves the series.

While the module is pre-1.0, an observable behavior change needs `#minor`.
For documentation-only work that should not publish a release, maintainers
can put `[skip release]` in the head commit message that lands on `main`.

Because this is a Go module, a major bump past v1 also means moving the module
path to `/v2`. Reach for `#major` deliberately.
