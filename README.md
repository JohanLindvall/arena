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
var a arena.StringArena // the zero value is usable, with 64 KiB chunks

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

## The two types

`Arena[T]` stores slices of any `T`. `StringArena` is an `Arena[byte]` with the
string entry points added — it exists because a method cannot narrow its
receiver's type parameter, so `Intern` cannot live on `Arena` itself. Everything
`Arena[byte]` does is promoted onto it, and the embedded field is addressable as
`a.Arena` for code that wants the plain arena.

```go
a := arena.New[Sample](1 << 20)   // 1 MiB chunks of Sample
s := arena.NewStringArena(4096)   // 4 KiB chunks, plus Intern/StrRef/Str
f := arena.Make[float64](4096)    // the same, as a value rather than a pointer
```

## Storing a value

These four copy the value in, and differ only in what you get back. (`Reserve`,
below, is the one entry point that does not copy.)

| Call | Returns | Use it when |
| --- | --- | --- |
| `Arena[T].Append([]T) []T` | a `[]T` view | you hold few enough views that pointers are free |
| `Arena[T].AppendRef([]T) Ref[T]` | a pointer-free descriptor | you retain a great many descriptors across the batch |
| `StringArena.Intern(string) string` | a `string` view | the value is a string and you hold a bounded number |
| `StringArena.StrRef(string) Ref[byte]` | a pointer-free descriptor | the value is a string and you retain a great many |

`Value` resolves a `Ref[T]` back to a `[]T`; `Str` resolves one back to a
`string`. They are the same descriptor, so the byte and string sides
interoperate freely. All four store through one path, so they agree on
everything except what they hand back.

Every view is **one contiguous slice**. A value that does not fit the current
chunk starts a new chunk rather than being split, so nothing has to be
reassembled on the way out.

Views stay valid until the next `Reset` or `Release`. Chunks are never
reallocated once allocated, so storing more values never invalidates a view
handed out earlier.

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

### Why `Ref` exists

`Ref[T]` locates a value by chunk index and range instead of by pointer:

```go
refs := make([]arena.Ref[byte], 0, len(lines))
for _, line := range lines {
    refs = append(refs, a.StrRef(line))
}
...
for _, r := range refs {
    if !r.Empty() {
        w.WriteString(a.Str(r))
    }
}
```

The extra indirection on every read buys two things. A `[]Ref[T]` holds no
pointers, so it is allocated noscan and the garbage collector skips it entirely
— where a `[][]byte` or `[]string` puts a pointer in every element and is walked
on every cycle. And a `Ref` is 12 bytes against a string header's 16 or a slice
header's 24, which for a caller that retains descriptors is the same saving
twice over. Both hold whatever `T` is: `Ref[T]` is 12 pointer-free bytes even
when `T` itself is full of pointers.

The type parameter is a phantom — it carries no field, and is there so a `Ref`
cannot be resolved against an arena of some other element type.

The zero `Ref` is the absent value: `AppendRef` and `StrRef` return it for empty
input, `Empty` reports it, `Value` resolves it to `nil` and `Str` to `""`.

### What a `Ref` can address

Three `int32`s are what make a `Ref` small, and also what bound it. The bound is
2³¹−1 **elements**, not bytes, so it scales with the width of `T` — 2 GiB for an
`Arena[byte]`, 16 GiB for an `Arena[int64]`. Two things can reach it: a single
value stored through `AppendRef`/`StrRef`, and an arena `New` was given a chunk
that wide. A third, 2³¹ chunks at once, is out of reach at any sane chunk size.

**`AppendRef` panics rather than let the conversion wrap**, so crossing the bound
is loud. The check measures free: the uniform store path already caps the offset
at the chunk's own capacity, so only an oversized value or an over-wide chunk can
get near it, and the branch is never taken in ordinary use.

It is worth naming what the panic stands in for. Unguarded, a length between 2³¹
and 2³² wraps `end` negative, so `Empty` reports the value **absent** and `Value`
returns `nil` — the value is gone with nothing said. Past 2³² it wraps to a small
positive number and `Value` returns a **truncated prefix**.

`Append` and `Intern` carry no such limit — a slice or string header holds no
`int32`. The bound belongs to the descriptor, not to the arena, so they are what
to reach for when a value could approach it.

## Rewinding

| Call | Bytes | Uniform chunks | Oversized chunks |
| --- | --- | --- | --- |
| `Reset()` | dropped | **kept**, ready to be refilled | **freed** |
| `Release()` | dropped | **freed** | **freed** |

`Reset` is for an owner about to run the next batch: re-allocating the chunks
would be the largest allocation it makes, so it does not. `Release` is for an
owner that outlives its bytes — a pooled writer parked empty between batches,
which should retain its per-value bookkeeping and none of the values.

Two counters describe the arena, both in bytes whatever `T` is. `Size` is the
bytes stored since the last rewind, which is what you check against a payload
budget; `Retained` is the chunk capacity holding them, which is the actual
footprint. A burst grows the chunk list and `Reset` never shrinks the uniform
part of it, so `Retained` is the number a trim policy should watch. Mid-batch it
counts any oversized chunks in flight; between batches it is the uniform
capacity that carried over.

## Sizing the chunk

`New[T](chunkBytes)` takes a **byte budget**, not a count of elements, and
divides it down to a whole number of `T` (never below one). `Make[T]` is the
same thing as a value rather than a pointer, for an arena that lives as a field
or a local; `StringArena{Arena: arena.Make[byte](n)}` is what `NewStringArena`
builds. A wider `T` means
fewer of them per chunk, not a bigger chunk — so the 64 KiB zero value stays
sane for a 64-byte struct instead of quietly becoming 4 MiB.

Chunk size decides the tail waste: a value that does not fit the current chunk
starts a new one and strands the remainder, so size the chunk to the values.
Interning short strings is comfortable at the default; accumulating large rows
or blocks wants something closer to 1 MiB.

A value larger than a whole chunk gets a chunk of its own, sized to fit it
exactly. All four entry points do the same thing with one, so an oversized value
is `Ref`-addressable like any other. `Reset` **drops** those chunks instead of
recycling them — one was made to fit a single large value and is the wrong shape
for anything else, so keeping it would leave that value's memory in the reusable
set for as long as the arena lives. What survives a `Reset` is uniform, however
lumpy the batch that just ran was.

## The rules

The arena trades safety for speed in three specific places, and it is on the
caller to hold up the other end:

1. **Not safe for concurrent use.** One goroutine at a time, or your own lock.
2. **Every view must be dead before `Reset` or `Release`.** Nothing checks this.
   A view read afterwards sees whatever the next batch wrote there — and a
   region from `Reserve` is a view like any other.
3. **Never mutate what you were handed.** The views alias the arena's own
   storage, and `Intern` and `Str` hand back strings over bytes it still owns.

What the arena saves is object *count*, not scan work: for a `T` that contains
pointers the chunks are still scanned, they are simply a handful of large
objects rather than one per value. For a pointer-free `T` — bytes included — the
chunks are noscan and the collector ignores them outright.

Run your tests under `-race`; this package's own suite does, on every supported
Go version, on Linux, macOS and Windows.

## Benchmarks

`go test -bench=. ./...` — medians of five runs on one machine (Intel Core
Ultra 9 185H, Go 1.26, linux/amd64). Indicative, not a promise.

Interning against the obvious alternative, one heap-allocated copy per value,
with both sides holding a batch of 4096 live — a copy that dies immediately gets
stack-allocated and proves nothing:

| Value | `StringArena.Intern` | One heap copy per value |
| --- | --- | --- |
| 16 B | 6.6 ns/op, 0 allocs | 15.5 ns/op, 1 alloc |
| 256 B | 8.9 ns/op, 0 allocs | 60.1 ns/op, 1 alloc |
| 4 KiB | 152 ns/op, 0 allocs | 647 ns/op, 1 alloc |

At 4 KiB a batch no longer fits the chunks, so that row is dominated by chunk
allocation and moves by tens of percent between runs; the two smaller ones are
stable to a few percent.

The collector is the other half of the story. Holding 2²⁰ descriptors live and
timing one full GC cycle over each form — same bytes in the arena either way,
only the descriptor slice differs:

| Descriptors retained | Width | GC cycle |
| --- | --- | --- |
| `[]Ref[byte]` | 12 B, noscan | **254 µs** |
| `[][]byte` | 24 B, scanned | 2081 µs |
| `[]string` | 16 B, scanned | 2393 µs |

The element type costs nothing. Storing 256 bytes per call through arenas of
different `T`, where the struct case also carries a pointer and so has scanned
chunks:

| Element type | Store |
| --- | --- |
| `byte` | 9.0 ns/op, 0 allocs |
| `int64` | 8.8 ns/op, 0 allocs |
| `struct{int64; float64; string}` | 10.1 ns/op, 0 allocs |

## Documentation

Full API documentation is on
[pkg.go.dev](https://pkg.go.dev/github.com/JohanLindvall/arena), including
runnable examples.

## License

[MIT](LICENSE)
