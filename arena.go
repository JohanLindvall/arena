// Package arena provides chunk-backed storage for values whose lifetime is one batch.
//
// Values are copied in and handed back as views over the copy; nothing is freed
// individually. The whole arena is rewound at once — Reset to reuse the chunks, Release to
// drop them — which is what makes it cheap: allocation is a bump within the current chunk,
// and a batch's worth of values costs a handful of chunk allocations rather than one per
// value.
//
// Arena is generic in its element type, so it stores slices of any T. Strings are the one
// case the generic form cannot express, since a method cannot narrow its receiver's type
// parameter: [StringArena] is an Arena[byte] with the string entry points added.
//
// It is not safe for concurrent use. Every view handed out must be dead before Reset or
// Release, and callers must not mutate what they were handed.
package arena

import (
	"slices"
	"unsafe"
)

// defaultChunkBytes is the backing-chunk size used by the zero value. It is a byte budget
// rather than a count of elements, so the zero Arena costs the same 64 KiB per chunk
// whatever T is, instead of scaling with the width of T behind the caller's back.
//
// New overrides it: a columnar block accumulator wants 1 MiB chunks, where a caller
// interning short label strings is better served by the 64 KiB default.
const defaultChunkBytes = 1 << 16

// elemSize is the width of one T in bytes.
func elemSize[T any]() int { return int(unsafe.Sizeof(*new(T))) }

// chunkElems converts a byte budget into a chunk capacity in elements. A chunk always
// holds at least one T however wide T is, which also keeps a zero-width T from dividing
// by zero.
func chunkElems[T any](chunkBytes int) int {
	return max(1, chunkBytes/max(1, elemSize[T]()))
}

// Arena copies slices of T into uniform chunk-backed storage and hands back views over
// the copies. A view stays valid until Reset: chunks are never reallocated once allocated
// (a value that does not fit the current chunk goes into a fresh one), so storing more
// never invalidates earlier views. Reset rewinds the arena to reuse the existing chunks.
// It is not safe for concurrent use.
//
// What the arena saves is object count, not scan work: for a T that contains pointers the
// chunks are still scanned, they are simply a handful of large objects rather than one per
// value. For a pointer-free T the chunks are noscan and the collector ignores them
// outright.
type Arena[T any] struct {
	chunks [][]T
	idx    int // index of the chunk currently being filled
	// chunk is the backing-chunk capacity in ELEMENTS. Zero means it has not been worked
	// out yet, so the zero Arena is usable without a constructor; chunkLen fills it in on
	// first use, which keeps the byte-to-element division off the store path.
	chunk int
	// size is the elements stored since the last Reset or Release, oversized standalone
	// copies included. A caller that bounds a payload counts this (through Size, which
	// scales it to bytes), not Retained: it is the data, where Retained is the capacity
	// holding it.
	size int
}

// New returns an arena with a chunk size of its own, given as a byte budget and rounded
// down to a whole number of T. Use it when the values are large relative to the default
// chunk — chunk size decides the tail waste, since a value that does not fit the current
// chunk starts a new one and strands the remainder.
func New[T any](chunkBytes int) *Arena[T] {
	return &Arena[T]{chunk: chunkElems[T](chunkBytes)}
}

// chunkLen is the backing-chunk capacity in elements, defaulted on first use.
func (a *Arena[T]) chunkLen() int {
	if a.chunk == 0 {
		a.chunk = chunkElems[T](defaultChunkBytes)
	}
	return a.chunk
}

// Append copies v into the arena and returns a stable view over the copy. The view stays
// valid until Reset or Release, and is always ONE contiguous slice: a value that does not
// fit the current chunk starts a new one rather than being split, so nothing has to be
// reassembled on the way out.
//
// Values larger than a chunk are not arena-backed: they get a standalone copy, so the
// reusable chunks stay a uniform size and one huge value cannot pin an oversized chunk for
// the arena's lifetime.
func (a *Arena[T]) Append(v []T) []T {
	if len(v) == 0 {
		return nil
	}
	a.size += len(v)
	if len(v) > a.chunkLen() {
		return slices.Clone(v)
	}
	// Skip chunks that lack room; never grow an existing chunk in place (that could
	// reallocate and invalidate views already handed out from it).
	for a.idx < len(a.chunks) && cap(a.chunks[a.idx])-len(a.chunks[a.idx]) < len(v) {
		a.idx++
	}
	if a.idx == len(a.chunks) {
		a.chunks = append(a.chunks, make([]T, 0, a.chunkLen()))
	}
	cur := a.chunks[a.idx]
	start := len(cur)
	cur = append(cur, v...) // in-cap append: same backing array, no reallocation
	a.chunks[a.idx] = cur
	return cur[start:]
}

// Ref locates a value in the arena WITHOUT a pointer to it — a chunk index and a range
// within that chunk. Value resolves it. The type parameter is a phantom: it carries no
// field, and exists so a Ref cannot be resolved against an arena of some other element
// type.
//
// The pointer-free part is the point, and it is worth the extra indirection on every read.
// A []Ref is allocated noscan, so the GC never walks it and a sort never pays a write
// barrier moving one; the []T form that Append returns puts a pointer in every element,
// and a caller holding millions of them turns its largest structure into scan work on every
// cycle. Measured on a production ingest service: the []byte form put
// runtime.gcBgMarkWorker at 11.1% of the process against 0.5% elsewhere in the tree. It is
// also 12 bytes against a slice header's 24, which for a caller that retains descriptors is
// the same saving twice over. Both hold whatever T is, pointer-bearing ones included.
//
// The zero Ref is the ABSENT value: a stored value is never zero elements long (AppendRef
// returns the zero Ref for empty input), so end <= off cannot name a real one.
//
// int32 bounds both fields well below 2^31 — the chunk index by the arena's total size, the
// offsets by the chunk size, except in an oversized chunk where they are bounded by the
// single value that made it.
type Ref[T any] struct {
	chunk    int32
	off, end int32
}

// Empty reports whether r names no value — the absent/null descriptor.
func (r Ref[T]) Empty() bool { return r.end <= r.off }

// AppendRef copies v into the arena and returns a pointer-free Ref to the copy, for a
// caller that keeps many descriptors and does not want them scanned (see [Ref]).
//
// Unlike Append, an oversized value goes into a chunk of its own rather than a standalone
// copy, because a Ref can only address chunk storage. That chunk is NOT uniform, so it is
// kept by Reset and only freed by Release — fine for a caller that releases per batch, worth
// knowing for one that does not.
func (a *Arena[T]) AppendRef(v []T) Ref[T] {
	if len(v) == 0 {
		return Ref[T]{}
	}
	a.size += len(v)
	for a.idx < len(a.chunks) && cap(a.chunks[a.idx])-len(a.chunks[a.idx]) < len(v) {
		a.idx++
	}
	if a.idx == len(a.chunks) {
		a.chunks = append(a.chunks, make([]T, 0, max(len(v), a.chunkLen())))
	}
	cur := a.chunks[a.idx]
	start := len(cur)
	a.chunks[a.idx] = append(cur, v...)
	return Ref[T]{chunk: int32(a.idx), off: int32(start), end: int32(start + len(v))}
}

// Value resolves r against the arena, or nil for the absent descriptor.
func (a *Arena[T]) Value(r Ref[T]) []T {
	if r.Empty() {
		return nil
	}
	return a.chunks[r.chunk][r.off:r.end]
}

// Size reports the bytes stored since the last Reset or Release.
func (a *Arena[T]) Size() int { return a.size * elemSize[T]() }

// Reset rewinds the arena so its chunks are reused for the next round. The caller
// must ensure every view previously handed out is dead before calling Reset.
func (a *Arena[T]) Reset() {
	for i := range a.chunks {
		a.chunks[i] = a.chunks[i][:0]
	}
	a.idx = 0
	a.size = 0
}

// Release drops the chunks outright rather than rewinding them, for a caller that keeps the
// STRUCTURE around a batch but not the batch's bytes — a pooled writer parks itself drained
// and releases its arena first, so what is retained is the per-value bookkeeping and never
// the values. Every view handed out must be dead first, exactly as for Reset.
func (a *Arena[T]) Release() {
	a.chunks = nil
	a.idx = 0
	a.size = 0
}

// Retained reports the bytes the arena keeps across Resets — its chunk capacity.
// Chunks are uniform (oversized values get standalone clones that die with their
// batch), so this is exact, and it is the footprint a trim policy should judge (a burst
// grows the chunk list; Reset rewinds but never shrinks it).
func (a *Arena[T]) Retained() int {
	// Summed rather than len(chunks)*chunkLen: AppendRef gives an oversized value a chunk
	// of its own, so the chunks are not always uniform and the multiplication would lie.
	n := 0
	for _, c := range a.chunks {
		n += cap(c)
	}
	return n * elemSize[T]()
}
