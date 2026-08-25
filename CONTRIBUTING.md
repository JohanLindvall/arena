# Contributing

## Running the checks

What CI runs, in the order it runs it:

```sh
go build ./...
go vet ./...
go test -race -shuffle=on ./...     # -race matters: the package hands out unsafe views
go mod tidy -diff                   # must be a no-op
golangci-lint run                   # v2, config in .golangci.yml
go test -run='^$' -bench=. ./...
```

`golangci-lint` is pinned in [.github/workflows/ci.yml](.github/workflows/ci.yml);
install the same version so local results match.

New behaviour needs a test, and anything worth explaining in the README is worth
a runnable `Example` in [example_test.go](example_test.go) — those are compiled,
run and diffed against their `// Output:` on every CI run, so they cannot drift
from the code the way prose does.

## Releases are automatic

Every commit that lands on `main` and passes CI gets a tag and a GitHub release,
cut by [.github/workflows/release.yml](.github/workflows/release.yml). Patch by
default. To ask for something else, put a marker anywhere in a commit message in
the push:

| Marker | Bump |
| --- | --- |
| `#minor` | minor |
| `#major`, or a line starting `BREAKING CHANGE:` | major |
| `[skip release]` in the head commit | nothing is tagged |

Don't tag by hand — the workflow reads the highest existing `vX.Y.Z` tag to
decide the next one, so a manual tag silently moves the series.

Because this is a Go module, a major bump past v1 also means moving the module
path to `/v2`. Reach for `#major` deliberately.
