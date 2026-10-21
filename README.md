# arena

[![CI](https://github.com/JohanLindvall/arena/actions/workflows/ci.yml/badge.svg?branch=main&event=push)](https://github.com/JohanLindvall/arena/actions/workflows/ci.yml?query=branch%3Amain+event%3Apush)
[![Go Reference](https://pkg.go.dev/badge/github.com/JohanLindvall/arena.svg)](https://pkg.go.dev/github.com/JohanLindvall/arena)
[![Release](https://img.shields.io/github/v/release/JohanLindvall/arena?sort=semver)](https://github.com/JohanLindvall/arena/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Reusable batch storage for Go slices and strings, reducing allocation and GC
scanning overhead with 12-byte pointer-free references.**

**arena** packs values that share a lifetime into reusable chunks. Store the
strings, byte slices, or rows for a batch, use them as ordinary Go values, then
call `Reset` to reuse the storage for the next batch.

- **Fewer heap objects:** allocate chunks instead of a separate backing array
  for every value.
- **Lower GC CPU load when holding millions of strings:** retain the `Ref[byte]`
  descriptors returned by `AppendStringRef` and resolve them with `StringValue`
  as needed. The collector skips their backing array during pointer scanning,
  avoiding the per-string scan work of a live `[]string` on every GC cycle.
- **Fill values in place:** `Reserve` gives you writable storage with an exact
  capacity, avoiding a copy from a temporary buffer.

Go 1.24+ · usable zero value · no dependencies outside the standard library at runtime

[Quick start](#quick-start) · [API guide](#storing-a-value) ·
[Lifetime rules](#the-rules) · [Benchmarks](#benchmarks) ·
[Contributing](CONTRIBUTING.md)

## Quick start

In an existing Go module:

```sh
go get github.com/JohanLindvall/arena
```

For a scratch project, first run `mkdir arena-demo`, `cd arena-demo`, and
`go mod init example.com/arena-demo`, then the `go get` command above.

Save this as `main.go` in the scratch project:

```go
package main

import (
	"fmt"

	"github.com/JohanLindvall/arena"
)

func main() {
	var a arena.StringArena // the zero value is usable, with 64 KiB chunks

	for batch := range 2 {
		labels := make([]string, 0, 3)
		for _, key := range []string{"container_id", "level", "namespace"} {
			labels = append(labels, a.AppendString(key)) // copies key into the arena
		}
		fmt.Printf("batch %d: %v, %d bytes stored\n", batch, labels, a.Size())

		// All uses of labels are finished before reusing their storage.
		a.Reset()
	}
}
```

Run `go run .`:

```text
batch 0: [container_id level namespace], 26 bytes stored
batch 1: [container_id level namespace], 26 bytes stored
```

[**Run it in your browser**](https://pkg.go.dev/github.com/JohanLindvall/arena#example-package)
— the same loop as a runnable example on pkg.go.dev, built and checked against
its output on every CI run. Nothing to install.

**All views and references expire at `Reset` or `Release`.** Finish using a
batch before resetting, or [copy out the values you need to keep](#keeping-data-after-a-batch).
Use one arena per worker, and follow [the four lifetime and ownership rules](#the-rules).

## When to use arena

Arena is useful when profiling shows allocation or GC overhead in a workload
with a clear batch boundary: decoding records, building a columnar block, or
processing a group of log events.

Holding millions of strings can consume GC CPU time even when the collection
is no longer growing: each GC cycle still scans the pointers in its string
headers. `AppendStringRef` lets you keep those values as pointer-free
descriptors for the batch, removing that per-string scanning work. This
addresses the cost of keeping values live as well as the cost of allocating them.

| Your workload | Starting point |
| --- | --- |
| Strings or slices all expire together | `StringArena.AppendString` or `Arena[T].Append`, then `Reset` |
| GC CPU load from millions of retained strings in a batch | `AppendStringRef`, retain `[]Ref[byte]`, and resolve with `StringValue` as needed |
| Millions of retained slice references in a batch | `AppendRef`, resolved with `Value` through the same arena |
| A decoder knows a value's length before filling it | `Reserve(n)` |
| Values need independent lifetimes or individual deletion | Ordinary Go allocations |
| You need to deduplicate equal strings | A map or an interning table; `AppendString` copies every call |

The library uses ordinary Go heap allocations and works with the standard Go
toolchain. It does not require an experimental runtime or a build flag.
Measure your workload: chunks still allocate when they grow, and `Reset`
retains uniform chunk storage for reuse.

## The two types

`Arena[T]` stores slices of any `T`. `StringArena` is an `Arena[byte]` with the
string entry points added — it exists because a method cannot narrow its
receiver's type parameter, so `AppendString` cannot live on `Arena` itself.
Everything `Arena[byte]` does is promoted onto it, and the embedded field is
addressable as `a.Arena` for code that wants the plain arena.

```go
a := arena.New[Sample](1 << 20)   // 1 MiB chunks of Sample
s := arena.NewStringArena(4096)   // 4 KiB chunks, with string entry points
f := arena.Make[float64](4096)    // the same, as a value rather than a pointer
```

## Storing a value

These four copy the value in, and differ only in what you get back. (`Reserve`,
below, is the one entry point that does not copy.)

| Call | Returns | Use it when |
| --- | --- | --- |
| `Arena[T].Append([]T) []T` | a `[]T` view | you want direct slice access and hold a bounded number of views |
| `Arena[T].AppendRef([]T) Ref[T]` | a pointer-free descriptor | you retain a great many descriptors across the batch |
| `StringArena.AppendString(string) string` | a `string` view | the value is a string and you hold a bounded number |
| `StringArena.AppendStringRef(string) Ref[byte]` | a pointer-free descriptor | you retain many strings and want to reduce GC scanning CPU cost |

`Value` resolves a `Ref[T]` back to a `[]T`; `StringValue` resolves one back to a
`string`. They are the same descriptor, so the byte and string sides
interoperate freely. All four store through one path, so they agree on
everything except what they hand back.

Copies are shallow: pointer-bearing elements still refer to the original objects.
`AppendString` copies each string on every call; it does not deduplicate equal
strings.

Every view is **one contiguous slice**. A value that does not fit the current
chunk starts a new chunk rather than being split, so nothing has to be
reassembled on the way out.

Views stay valid until the next `Reset` or `Release`. Chunks are never
reallocated once allocated, so storing more values never invalidates a view
handed out earlier.

### Updating existing callers

The string methods have been renamed to describe their operations:

| Previous name | Current name |
| --- | --- |
| `Intern` | `AppendString` |
| `StrRef` | `AppendStringRef` |
| `Str` | `StringValue` |

Update calls and method references to the new names. Storage and lifetime
behavior is the same.

### Building a value in place

`Reserve(n)` is `Append` without the copy: it hands back a `[]T` of length 0 and
capacity **exactly** `n`, backed by the arena, for a value you are building
rather than one you already hold. A decoder that learns an array's length before
its elements reserves the backing once and appends into it, paying neither a
per-value allocation nor a copy out of a scratch buffer.

```go
p := a.Reserve(len(row))          // len 0, cap exactly len(row)
for _, v := range row {
    p = append(p, v)              // fills the arena's own storage
}
```

The capacity is exactly `n` rather than the rest of the chunk, so overfilling
reallocates to the heap the way any other full slice does and can never write
over the neighbour. Everything else matches `Append`: the region is yours alone,
it stays valid until `Reset` or `Release`, and a reservation too large for a
chunk gets a chunk of its own. `Reserve` and the copying entry points share one
placement step and mix freely on one arena.

`Reserve` **does not clear** what it hands back — a caller about to fill the
region would pay for the zeroing twice. Before the first `Reset` every chunk
comes from `make` and so reads as zero; once `Reset` hands a chunk out again it
holds whatever the previous batch left. `clear` it if you read before writing,
or hand out a region you only partly fill.

Clear the **full reservation**, since the returned slice has length zero:

```go
p := a.Reserve(n)
clear(p[:cap(p)])
p = p[:n]                       // safe to read all n elements, including unwritten ones
```

### Why `Ref` exists

`Ref[T]` locates a value by chunk index and range instead of by pointer:

```go
refs := make([]arena.Ref[byte], 0, len(lines))
for _, line := range lines {
    refs = append(refs, a.AppendStringRef(line))
}
...
for _, r := range refs {
    if !r.Empty() {
        w.WriteString(a.StringValue(r))
    }
}
```

The extra indirection on every read buys two things. A `[]Ref[T]` holds no
pointers, so its backing array is allocated noscan and skipped during GC scanning
— where a `[][]byte` or `[]string` puts a pointer in every element and is walked
on every cycle. And a `Ref` is 12 bytes against a string header's 16 or a slice
header's 24, which for a caller that retains descriptors is the same saving
twice over. Both hold whatever `T` is: `Ref[T]` is 12 pointer-free bytes even
when `T` itself is full of pointers.

For `StringArena`, the byte chunks also contain no pointers. The collector
still scans the arena's chunk headers and any live string views, but retaining
`[]Ref[byte]` removes the pointer per stored string. Keeping `AppendString`
results in a `[]string` does not remove that scan work; neither does keeping
all the original input strings or the views returned by `StringValue`.
Retain descriptors and resolve views only as needed. The Go GC guide explains how
[pointers in the live heap contribute to GC CPU cost](https://go.dev/doc/gc-guide#Understanding_costs).

The type parameter is a phantom — it carries no field, and is there so a `Ref`
cannot be resolved against an arena of some other element type.

The zero `Ref` is the absent value: `AppendRef` and `AppendStringRef` return it
for empty input, `Empty` reports it, `Value` resolves it to `nil` and
`StringValue` to `""`.

### What a `Ref` can address

Three `int32`s are what make a `Ref` small, and also what bound it. The bound is
2³¹−1 **elements**, not bytes, so it scales with the width of `T` — 2 GiB for an
`Arena[byte]`, 16 GiB for an `Arena[int64]`. Two things can reach it: a single
value stored through `AppendRef`/`AppendStringRef`, and an arena `New` was given
a chunk that wide. A third, 2³¹ chunks at once, is out of reach at any sane
chunk size.

**`AppendRef` and `AppendStringRef` panic rather than let the conversion wrap**,
so crossing the bound is loud. The check measures free: the uniform store path
already caps the offset at the chunk's own capacity, so only an oversized
value or an over-wide chunk can get near it, and the branch is never taken
in ordinary use.

The check happens after storing the value. Recovering the panic leaves that
value counted in `Size` until the next rewind.

It is worth naming what the panic stands in for. Unguarded, a length between 2³¹
and 2³² wraps `end` negative, so `Empty` reports the value **absent** and `Value`
returns `nil` — the value is gone with nothing said. Past 2³² it wraps to a small
positive number and `Value` returns a **truncated prefix**.

`Append` and `AppendString` carry no such limit — a slice or string header holds no
`int32`. The bound belongs to the descriptor, not to the arena, so they are what
to reach for when a value could approach it.

## Rewinding

| Call | `Size()` afterwards | Uniform chunks | Oversized chunks |
| --- | --- | --- | --- |
| `Reset()` | 0 | **kept**, contents unchanged | **dropped** |
| `Release()` | 0 | **dropped** | **dropped** |

`Reset` is for an owner about to run the next batch: re-allocating the chunks
would be the largest allocation it makes, so it does not. `Release` is for an
owner that outlives its bytes — a pooled writer parked empty between batches,
which should retain its per-value bookkeeping and none of the values.

`Reset` does not erase stored data. If `T` contains pointers, old elements in
reused chunks keep their targets alive until overwritten or the chunks are
released. Use `Release` when those targets should become collectible between
batches. Dropping a chunk makes it eligible for garbage collection once no
other references to it remain; it does not immediately return memory to the OS.

Two counters describe the arena, both in bytes whatever `T` is. `Size` is the
bytes stored since the last rewind, which is what you check against a payload
budget. It includes unfilled reservations, but excludes failed allocations.
`Retained` is the chunk capacity holding them, excluding chunk headers, allocator
overhead, and objects referenced by `T`. A burst grows the chunk list and `Reset`
never shrinks the uniform part of it, so `Retained` is the number a trim policy
should watch. Mid-batch it
counts any oversized chunks in flight; between batches it is the uniform
capacity that carried over.

## Sizing the chunk

`New[T](chunkBytes)` takes a **byte budget**, not a count of elements, and
divides it down to a whole number of `T` (never below one, even with a zero or
negative budget). `Make[T]` is the
same thing as a value rather than a pointer, for an arena that lives as a field
or a local; `StringArena{Arena: arena.Make[byte](n)}` is what `NewStringArena`
builds. A wider `T` means
fewer of them per chunk, not a bigger chunk — so the 64 KiB zero value stays
sane for a 64-byte struct instead of quietly becoming 4 MiB.

Chunk size decides the tail waste: a value that does not fit the current chunk
starts a new one and strands the remainder, so size the chunk to the values.
Storing short strings is comfortable at the default; accumulating large rows
or blocks wants something closer to 1 MiB.

A value larger than a whole chunk gets a chunk of its own, sized to fit it
exactly. Copying and reserving follow the same rule, so an oversized value
is `Ref`-addressable like any other. `Reset` **drops** those chunks instead of
recycling them — one was made to fit a single large value and is the wrong shape
for anything else, so keeping it would leave that value's memory in the reusable
set for as long as the arena lives. What survives a `Reset` is uniform, however
lumpy the batch that just ran was.

## The rules

The caller must observe these rules:

1. **Not safe for concurrent use.** One goroutine at a time, or your own lock.
2. **Every view and `Ref` expires at `Reset` or `Release`.** Nothing checks this.
   A view read afterwards sees whatever the next batch wrote there — and a
   region from `Reserve` is a view like any other. Resolve a nonempty `Ref` only
   against the arena and batch that created it; misuse can return unrelated data
   or panic.
3. **Views from `Append` and `Value` are read-only.** Do not modify or append to
   them: their capacity can extend into neighbouring values, including bytes
   exposed as immutable strings by `AppendString` and `StringValue`. `Reserve`
   is the explicit exception: its region is yours to fill, with capacity limited
   to its size.
4. **Do not copy an arena after first use.** This includes `StringArena` and
   structs containing either type. Copies share storage but track placement
   independently, so they can overwrite each other's values. Pass a pointer.

For a `T` that contains pointers, chunking reduces object *count*, but the
collector still scans the pointers in the stored elements. For a pointer-free
`T` — bytes included — the chunk contents need no pointer scanning. Retaining
`Ref[T]` descriptors also removes the per-value pointers from the descriptor
slice, regardless of `T`; it does not remove pointers inside `T` itself.

Run your tests under `-race`; this package's own suite does, on every supported
Go version, on Linux, macOS and Windows.

## Keeping data after a batch

Copy a value **before** `Reset` or `Release` if it must outlive the batch.
Assigning a slice or string to another variable only copies its header; it
still refers to arena storage.

```go
var a arena.StringArena
view := a.AppendString("keep me")
owned := strings.Clone(view) // import "strings"; this copy has its own lifetime
a.Reset()
a.AppendString("next batch") // can reuse the old view's storage
fmt.Println(owned)           // keep me
```

For slices, use `bytes.Clone` or `slices.Clone` on the view. These are shallow
copies: pointer-bearing elements still refer to the same objects. Resolve a
`Ref` and copy its value before resetting; copying the descriptor does not
extend its lifetime.

[Run the copy-out example](https://pkg.go.dev/github.com/JohanLindvall/arena#example-StringArena.AppendString-Clone).

## Benchmarks

`go test -bench=. ./...` — medians of five runs on one machine (Intel Core
Ultra 9 185H, Go 1.26, linux/amd64). Indicative, not a promise.

Storing against the obvious alternative, one heap-allocated copy per value,
with both sides holding a batch of 4096 live — a copy that dies immediately gets
stack-allocated and proves nothing:

| Value | `StringArena.AppendString` | One heap copy per value |
| --- | --- | --- |
| 16 B | 6.6 ns/op, 0 allocs | 15.5 ns/op, 1 alloc |
| 256 B | 8.9 ns/op, 0 allocs | 60.1 ns/op, 1 alloc |
| 4 KiB | 152 ns/op, 0 allocs | 647 ns/op, 1 alloc |

The 4 KiB row moves by tens of percent between runs; the two smaller ones are
stable to a few percent. That batch occupies 16 MiB across 256 default chunks.
The first batch allocates them; subsequent batches reuse them through `Reset`.

The collector is the other half of the story. Holding 2²⁰ (1,048,576)
descriptors live and timing one full GC cycle over each form — same bytes in
the arena either way, only the descriptor slice differs. This isolates the
cost of retaining references after the values have already been stored:

| Descriptors retained | Width | GC cycle elapsed time |
| --- | --- | --- |
| `[]Ref[byte]` | 12 B, noscan | **254 µs** |
| `[][]byte` | 24 B, scanned | 2081 µs |
| `[]string` | 16 B, scanned | 2393 µs |

These are elapsed times for forced GC cycles, not measurements of total
application CPU usage; profile your workload to measure its CPU savings.

The element type costs nothing. Storing 256 bytes per call through arenas of
different `T`, where the struct case also carries a pointer and so has scanned
chunks:

| Element type | Store |
| --- | --- |
| `byte` | 9.0 ns/op, 0 allocs |
| `int64` | 8.8 ns/op, 0 allocs |
| `struct{int64; float64; string}` | 10.1 ns/op, 0 allocs |

To repeat the measurements on your machine:

```sh
go test -run='^$' -bench=. -benchmem -count=5 ./...
```

Compare changes on the same machine with interleaved runs. The benchmark
source is in [arena_bench_test.go](arena_bench_test.go); the contributor guide
explains [how to compare performance](CONTRIBUTING.md#performance-changes).

## Help and contributing

Full API documentation is on
[pkg.go.dev](https://pkg.go.dev/github.com/JohanLindvall/arena), including
runnable examples. For usage questions, bug reports, or ideas, start with
[the issue chooser](https://github.com/JohanLindvall/arena/issues/new/choose).
[SUPPORT.md](SUPPORT.md) explains what to include.

Documentation improvements, small reproductions, examples, and benchmark
comparisons are welcome. You do not need to be an allocator expert to help.
The [contributor guide](CONTRIBUTING.md) covers setup, the source layout, and
the checks to run before a pull request. Please follow the
[code of conduct](CODE_OF_CONDUCT.md).

Maintained by [Johan Lindvall](https://github.com/JohanLindvall) and
[contributors](https://github.com/JohanLindvall/arena/graphs/contributors).
If arena is useful to you, consider starring the repository and sharing it
with someone working on a similar workload.

## Security

Report vulnerabilities privately, as [SECURITY.md](SECURITY.md) describes —
not in a public issue.

## License

[MIT](LICENSE)
