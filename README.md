# arena

[![CI](https://github.com/JohanLindvall/arena/actions/workflows/ci.yml/badge.svg)](https://github.com/JohanLindvall/arena/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/JohanLindvall/arena.svg)](https://pkg.go.dev/github.com/JohanLindvall/arena)
[![Go Report Card](https://goreportcard.com/badge/github.com/JohanLindvall/arena)](https://goreportcard.com/report/github.com/JohanLindvall/arena)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Chunk-backed storage for values whose lifetime is one batch.

Values are copied in and handed back as views over the copy. Nothing is freed
individually; the whole arena is rewound at once. That is what makes it cheap —
storing a value is a bump within the current chunk, so a batch's worth of values
costs a handful of chunk allocations instead of one allocation per value, and
the collector has a handful of objects to track instead of millions.

```
go get github.com/JohanLindvall/arena
```

Requires Go 1.24 or newer.

## Quick start

```go
var a arena.Arena // the zero value is usable, with 64 KiB chunks

for _, batch := range batches {
    labels := make([]string, 0, len(batch))
    for _, key := range batch {
        // A copy, so whatever the decoder underneath reuses its buffer for next
        // cannot show through.
        labels = append(labels, a.Intern(key))
    }

    emit(labels)

    // Every view above is dead from here, so the chunks can be handed out again.
    a.Reset()
}
```

## Storing a value

Three entry points, differing only in what you get back.

| Call | Returns | Use it when |
| --- | --- | --- |
| `Intern(string) string` | a `string` view | the value is a string — label keys, field names, enum values |
| `Append([]byte) []byte` | a `[]byte` view | the value is bytes, and you hold few enough views that pointers are free |
| `AppendRef([]byte) Ref` | a pointer-free descriptor | you retain a great many descriptors across the batch |

Every view is **one contiguous slice**. A value that does not fit the current
chunk starts a new chunk rather than being split, so nothing has to be
reassembled on the way out.

Views stay valid until the next `Reset` or `Release`. Chunks are never
reallocated once allocated, so storing more values never invalidates a view
handed out earlier.

### Why `Ref` exists

`Ref` locates a value by chunk index and range instead of by pointer, and
`Value` resolves it:

```go
refs := make([]arena.Ref, 0, len(lines))
for _, line := range lines {
    refs = append(refs, a.AppendRef(line))
}
...
for _, r := range refs {
    if !r.Empty() {
        w.Write(a.Value(r))
    }
}
```

The extra indirection on every read buys two things. A `[]Ref` holds no
pointers, so it is allocated noscan and the garbage collector skips it entirely
— where a `[][]byte` puts a pointer in every element and is walked on every
cycle. And a `Ref` is 12 bytes against a slice header's 24, which for a caller
that retains descriptors is the same saving twice over.

The zero `Ref` is the absent value: `AppendRef` returns it for empty input,
`Empty` reports it, and `Value` resolves it to `nil`. Three `int32`s is also
what bounds it — offsets by the chunk size, the chunk index by the arena's
total size.

## Rewinding

| Call | Bytes | Chunks |
| --- | --- | --- |
| `Reset()` | dropped | **kept**, ready to be refilled |
| `Release()` | dropped | **freed** |

`Reset` is for an owner about to run the next batch: re-allocating the chunks
would be the largest allocation it makes, so it does not. `Release` is for an
owner that outlives its bytes — a pooled writer parked empty between batches,
which should retain its per-value bookkeeping and none of the values.

Two counters describe the arena: `Size` is the bytes stored since the last
rewind, which is what you check against a payload budget, and `Retained` is the
chunk capacity holding them, which is the actual footprint. A burst grows the
chunk list and `Reset` never shrinks it, so `Retained` is the number a trim
policy should watch.

## Sizing the chunk

`New(chunkSize)` overrides the 64 KiB default. Chunk size decides the tail
waste: a value that does not fit the current chunk starts a new one and strands
the remainder, so size the chunk to the values. Interning short strings is
comfortable at the default; accumulating large rows or blocks wants something
closer to 1 MiB.

Values larger than a whole chunk are handled but win nothing. `Append` and
`Intern` give them a standalone copy that dies with the batch, keeping the
reusable chunks a uniform size so one huge value cannot pin an oversized chunk
for the arena's lifetime. `AppendRef` cannot do that — a `Ref` can only address
chunk storage — so it gives the value a chunk of its own, which is *not*
uniform and survives `Reset`. Only `Release` frees it.

## The rules

The arena trades safety for speed in three specific places, and it is on the
caller to hold up the other end:

1. **Not safe for concurrent use.** One goroutine at a time, or your own lock.
2. **Every view must be dead before `Reset` or `Release`.** Nothing checks this.
   A view read afterwards sees whatever the next batch wrote there.
3. **Never mutate what you were handed.** The views alias the arena's own
   storage, and `Intern` hands back a string over bytes it still owns.

Run your tests under `-race`; this package's own suite does, on every supported
Go version, on Linux, macOS and Windows.

## Benchmarks

`go test -bench=. ./...` — indicative numbers from one machine
(Intel Core Ultra 9 185H, Go 1.26, linux/amd64), not a promise:

| Benchmark | Arena | One heap copy per value |
| --- | --- | --- |
| Intern, 16 B | 6.7 ns/op, 0 allocs | 15.1 ns/op, 1 alloc |
| Intern, 256 B | 8.7 ns/op, 0 allocs | 64.5 ns/op, 1 alloc |
| Intern, 4 KiB | 121 ns/op, 0 allocs | 687 ns/op, 1 alloc |

Both sides hold a batch of 4096 values live, because a copy that dies
immediately gets stack-allocated and proves nothing.

The collector is the other half of the story. Holding 2²⁰ descriptors live and
timing one full GC cycle over each form:

| Descriptors retained | GC cycle |
| --- | --- |
| `[]Ref` (noscan) | 264 µs |
| `[][]byte` (scanned) | 2191 µs |

Same bytes in the arena either way; only the descriptor slice differs.

## Documentation

Full API documentation is on
[pkg.go.dev](https://pkg.go.dev/github.com/JohanLindvall/arena), including
runnable examples.

## License

[MIT](LICENSE)
