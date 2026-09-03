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

import "unsafe"

// defaultChunkBytes is the backing-chunk size used by the zero value. It is a byte budget
// rather than a count of elements, so the zero Arena costs the same 64 KiB per chunk
// whatever T is, instead of scaling with the width of T behind the caller's back.
//
// New overrides it: a columnar block accumulator wants 1 MiB chunks, where a caller
// interning short label strings is better served by the 64 KiB default.
const defaultChunkBytes = 1 << 16

// maxRefIndex is the largest offset or chunk index a Ref can hold, since it stores them as
// int32. AppendRef checks against it rather than letting the conversion wrap.
const maxRefIndex = 1<<31 - 1

// elemSize is the width of one T in bytes.
func elemSize[T any]() int { return int(unsafe.Sizeof(*new(T))) }

// chunkElems converts a byte budget into a chunk capacity in elements. A chunk always
// holds at least one T however wide T is, which also keeps a zero-width T from dividing
// by zero.
func chunkElems[T any](chunkBytes int) int {
	return max(1, chunkBytes/max(1, elemSize[T]()))
}

// Arena copies slices of T into chunk-backed storage and hands back views over the copies.
// A view stays valid until Reset: chunks are never reallocated once allocated (a value that
// does not fit the current chunk goes into a fresh one), so storing more never invalidates
// earlier views. Reset rewinds the arena to reuse the existing chunks. It is not safe for
// concurrent use.
//
// The chunks the arena reuses are all one size. A value too large for one gets a chunk of
// its own instead, which Reset drops rather than recycling, so a single huge value cannot
// leave an oversized chunk sitting in the reusable set for the arena's lifetime.
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
	// size is the elements stored since the last Reset or Release, whatever kind of chunk
	// they landed in. A caller that bounds a payload counts this (through Size, which
	// scales it to bytes), not Retained: it is the data, where Retained is the capacity
	// holding it.
	size int
}

// New returns an arena with a chunk size of its own, given as a byte budget and rounded
// down to a whole number of T. Use it when the values are large relative to the default
// chunk — chunk size decides the tail waste, since a value that does not fit the current
// chunk starts a new one and strands the remainder.
func New[T any](chunkBytes int) *Arena[T] {
	a := Make[T](chunkBytes)
	return &a
}

// Make is New as a VALUE rather than a pointer, for an arena that lives as a field of a
// struct the caller already has, or as a local — where New's allocation buys nothing.
// The zero Arena is usable and needs neither, so reach for Make only to choose a chunk
// size: `Make[T](n)` is to `var a Arena[T]` what `New[T](n)` is to `new(Arena[T])`.
//
// A StringArena takes its chunk size the same way, through the embedded field:
// `StringArena{Arena: Make[byte](n)}` is what NewStringArena builds.
func Make[T any](chunkBytes int) Arena[T] {
	return Arena[T]{chunk: chunkElems[T](chunkBytes)}
}

// chunkLen is the backing-chunk capacity in elements, defaulted on first use.
func (a *Arena[T]) chunkLen() int {
	if a.chunk == 0 {
		a.chunk = chunkElems[T](defaultChunkBytes)
	}
	return a.chunk
}

// place advances the arena past n elements and returns where they landed: the index of
// the chunk, the offset within it, and the region itself, length n. It is the whole of
// the placement policy, so the chunk invariants live here and nowhere else.
//
// src is either nil, meaning reserve n elements and leave them alone, or the value to
// copy in, in which case n is derived from it and the argument ignored. Append and
// AppendRef pass their value; Reserve passes nil and its count. Either way the count must
// be positive, which every caller screens for first.
//
// region comes back in the shape its caller wants — length n over the copy for the two
// storing entry points, length 0 with capacity exactly n for Reserve — because shaping it
// here is free (place is far past the inline budget and never inlines) where doing it in
// the caller is not: Append inlines at a cost of 80 against a budget of 80 and Reserve at
// 79, so a couple of nodes either way decides whether they cost a call.
func (a *Arena[T]) place(src []T, n int) (chunk, off int, region []T) {
	if src != nil {
		n = len(src)
	}
	a.size += n
	c := a.chunkLen()

	// A value too large for a uniform chunk gets one of its own, sized to fit it exactly
	// and parked at the end of the list WITHOUT moving idx. Leaving idx alone matters: the
	// new chunk is full the moment it is made, so stepping onto it would strand whatever
	// room is left in the chunk the next ordinary value is going to want. Reset drops these
	// rather than recycling them, which is what keeps the reusable chunks one size.
	if n > c {
		// The two arms differ only in a compiler optimisation, and it is worth real time:
		// a make the compiler can see is immediately and completely overwritten skips its
		// zeroing, and it can only see that when the make and the copy sit together with
		// the length written the same way in both. Splitting them — even just moving the
		// copy to the caller — costs a whole extra pass over the value: measured at
		// Append/65537 +27%, and 3.40 -> 4.33 us on a 64 KiB make+copy micro. Reserve
		// wants the zeroed allocation anyway, which is what its doc promises.
		if src == nil {
			big := make([]T, n) // exactly n, so isOversized can read it off the cap
			a.chunks = append(a.chunks, big)
			return len(a.chunks) - 1, 0, big[:0]
		}
		big := make([]T, len(src))
		copy(big, src)
		a.chunks = append(a.chunks, big)
		return len(a.chunks) - 1, 0, big
	}

	// Skip chunks that lack room; never grow an existing chunk in place (that could
	// reallocate and invalidate views already handed out from it).
	for a.idx < len(a.chunks) && cap(a.chunks[a.idx])-len(a.chunks[a.idx]) < n {
		a.idx++
	}
	if a.idx == len(a.chunks) {
		a.chunks = append(a.chunks, make([]T, 0, c))
	}
	cur := a.chunks[a.idx]
	off = len(cur)
	cur = cur[:off+n] // in-cap reslice: same backing array, no reallocation
	a.chunks[a.idx] = cur
	// Reserve's region is capped at exactly n so that overfilling it reallocates instead
	// of writing over the next value; the storing entry points keep the uncapped view
	// they have always handed back. The copy is guarded rather than unconditional over a
	// possibly-nil src because the chunk is already allocated here — there is no zeroing
	// to elide, so nothing is gained by keeping it adjacent, while an unguarded copy would
	// put a zero-length memmove CALL on Reserve's hot path.
	if src == nil {
		return a.idx, off, cur[off : off : off+n]
	}
	region = cur[off : off+n]
	copy(region, src)
	return a.idx, off, region
}

// isOversized reports whether c was made to hold a single value too large for a uniform
// chunk. The chunk's own capacity is the mark: store builds uniform chunks at exactly
// chunkLen and oversized ones at exactly the length of the value that forced them, so
// there is no side table that could fall out of step with the chunk list.
func (a *Arena[T]) isOversized(c []T) bool { return cap(c) > a.chunkLen() }

// Append copies v into the arena and returns a stable view over the copy. The view stays
// valid until Reset or Release, and is always ONE contiguous slice: a value that does not
// fit the current chunk starts a new one rather than being split, so nothing has to be
// reassembled on the way out.
//
// A value larger than a whole chunk gets a chunk of its own, which Reset drops rather than
// recycling — see [Arena].
func (a *Arena[T]) Append(v []T) []T {
	if len(v) == 0 {
		return nil
	}
	_, _, region := a.place(v, 0)
	return region
}

// Reserve hands back room for n elements — a slice of length 0 and capacity exactly n,
// backed by arena storage — for a caller that fills the region itself rather than copying
// a value in. It is Append without the copy, and the two can be mixed freely on one
// arena. n <= 0 reserves nothing and returns nil.
//
// Where Append is for a value the caller already has, Reserve is for one being built:
// a decoder that knows an array's length before its elements can reserve the backing once
// and append the elements into it, paying neither a per-value allocation nor a copy out of
// a scratch buffer.
//
// The capacity is exactly n, not the rest of the chunk, so filling the region past n
// reallocates to the heap the way any other full slice does and can never write over the
// neighbouring value. The region is the caller's alone: no later Append or Reserve
// overlaps it, and the view stays valid until Reset or Release exactly as Append's does.
// A value larger than a whole chunk gets a chunk of its own, as in Append.
//
// # Contents
//
// Reserve does not clear what it hands back, since a caller that is about to fill the
// region would pay for the zeroing twice. In a chunk the arena has not yet reused — every
// chunk before the first Reset — the memory comes from make and so reads as zero; after a
// Reset hands the same chunk out again it holds whatever the previous batch left there.
// A caller that reads before writing, or that hands out a region it only partly fills,
// wants `clear` over it (or an arena it never Resets).
//
// Size counts the whole reservation, filled or not: it is the room the batch has taken,
// which is what a caller bounding a payload is asking about.
func (a *Arena[T]) Reserve(n int) []T {
	if n <= 0 {
		return nil
	}
	_, _, region := a.place(nil, n)
	return region
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
// # Limits
//
// Three int32s are what makes a Ref small, and they are also what bounds it. The bound is
// 2^31-1 ELEMENTS rather than bytes, so it scales with the width of T: 2 GiB for an
// Arena[byte], but 16 GiB for an Arena[int64]. Two things can reach it — a single value
// stored through AppendRef or StrRef, and an arena that New was given a chunk that wide.
// A third, 2^31 chunks at once, is out of reach at any sane chunk size.
//
// AppendRef panics rather than let the conversion wrap, so crossing the bound is loud. It
// is worth knowing what the panic prevents: a length in [2^31, 2^32) wraps end negative,
// which Empty would report as ABSENT and Value resolve to nil, losing the value with
// nothing said; a length at or past 2^32 wraps to a small positive number, which Value
// would resolve to a truncated prefix.
//
// Append and Intern carry no such limit. They hand back a slice or a string header, neither
// of which holds an int32 — the bound belongs to the descriptor, not to the arena — so they
// are what to reach for when a value could approach it.
type Ref[T any] struct {
	chunk    int32
	off, end int32
}

// Empty reports whether r names no value — the absent/null descriptor.
func (r Ref[T]) Empty() bool { return r.end <= r.off }

// AppendRef copies v into the arena and returns a pointer-free Ref to the copy, for a
// caller that keeps many descriptors and does not want them scanned (see [Ref]). It stores
// exactly as Append does, the chunk of its own an oversized value gets included; the two
// differ only in what they hand back.
//
// It panics if the copy lands somewhere a Ref cannot describe — see the limits on [Ref].
// That is a loud stand-in for what the int32 conversion would otherwise do quietly, which
// is hand back a descriptor resolving to nothing or to the wrong bytes. Append has no such
// limit and is the way to store a value that large.
//
// The check is after the copy, not before it, so a recovered panic leaves the value stored
// but undescribed — wasted space in the current batch, which the next Reset reclaims, and
// never a corrupt arena.
func (a *Arena[T]) AppendRef(v []T) Ref[T] {
	if len(v) == 0 {
		return Ref[T]{}
	}
	i, off, _ := a.place(v, 0)
	end := off + len(v)
	// Only two things can reach here past the bound: a single value longer than 2^31-1
	// elements, and an arena built with New over a chunk that wide. The uniform path
	// cannot, because store only puts a value in a chunk with room for it, which caps end
	// at the chunk's own capacity.
	if end > maxRefIndex {
		panic("arena: value too large for a Ref to describe (limit 2^31-1 elements); Append has no such limit")
	}
	if i > maxRefIndex {
		panic("arena: too many chunks for a Ref to describe (limit 2^31-1)")
	}
	return Ref[T]{chunk: int32(i), off: int32(off), end: int32(end)}
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
//
// Oversized chunks are dropped rather than rewound. One was made to fit a single large
// value and is the wrong shape for anything else, so recycling it would leave that value's
// memory in the reusable set for as long as the arena lives. What survives a Reset is
// uniform, however lumpy the batch that just ran was.
func (a *Arena[T]) Reset() {
	kept := a.chunks[:0]
	for _, c := range a.chunks {
		if a.isOversized(c) {
			continue
		}
		kept = append(kept, c[:0])
	}
	clear(a.chunks[len(kept):]) // stop the backing array naming the chunks just dropped
	a.chunks = kept
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

// Retained reports the bytes the arena is holding — its chunk capacity, which is the real
// footprint a trim policy should judge (a burst grows the chunk list; Reset rewinds but
// never shrinks it).
//
// Mid-batch this counts any oversized chunks in flight. Reset drops those, so what it
// reports between batches is the uniform capacity that carries over.
func (a *Arena[T]) Retained() int {
	// Summed rather than len(chunks)*chunkLen: an oversized value gets a chunk of its own,
	// so the chunks are not always uniform and the multiplication would lie.
	n := 0
	for _, c := range a.chunks {
		n += cap(c)
	}
	return n * elemSize[T]()
}
