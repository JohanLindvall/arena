// Package arena provides chunk-backed storage for values whose lifetime is one batch.
//
// Values are copied in and handed back as views over the copy; nothing is freed
// individually. The whole arena is rewound at once — Reset to reuse the chunks, Release to
// drop them — which is what makes it cheap: allocation is a bump within the current chunk,
// and a batch's worth of values costs a handful of chunk allocations rather than one per
// value.
//
// It is not safe for concurrent use. Every view handed out must be dead before Reset or
// Release, and callers must not mutate what they were handed.
package arena

import (
	"bytes"
	"strings"
	"unsafe"
)

// b2s returns a string view over b without copying. The caller must guarantee b
// is not mutated for the lifetime of the returned string.
func b2s(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// defaultChunkSize is the backing-chunk size used by the zero value.
// New overrides it: a columnar block accumulator wants 1 MiB chunks, where a caller
// interning short label strings is better served by the 64 KiB default.
const defaultChunkSize = 1 << 16

// Arena copies strings into uniform chunk-backed storage and hands back string
// views over the copies. A view stays valid until Reset: chunks are never
// reallocated once allocated (a string that does not fit the current chunk goes
// into a fresh one), so interning more strings never invalidates earlier views.
// Reset rewinds the arena to reuse the existing chunks. It is not safe for
// concurrent use.
type Arena struct {
	chunks [][]byte
	idx    int // index of the chunk currently being filled
	// chunk is the backing-chunk size; zero means defaultChunkSize, so the zero Arena is
	// usable without a constructor.
	chunk int
	// size is the bytes stored since the last Reset or Release, oversized standalone
	// copies included. A caller that bounds a payload counts this, not Retained: it is the
	// data, where Retained is the capacity holding it.
	size int
}

// New returns an arena with a chunk size of its own. Use it when the values are large
// relative to the default chunk — chunk size decides the tail waste, since a value that does
// not fit the current chunk starts a new one and strands the remainder.
func New(chunkSize int) *Arena {
	return &Arena{chunk: chunkSize}
}

// chunkSize is the configured chunk size, or the default for a zero-value Arena.
func (a *Arena) chunkSize() int {
	if a.chunk == 0 {
		return defaultChunkSize
	}
	return a.chunk
}

// Append copies b into the arena and returns a stable view over the copy — the byte
// primitive Intern is written on top of. The view stays valid until Reset or Release, and
// is always ONE contiguous slice: a value that does not fit the current chunk starts a new
// one rather than being split, so nothing has to be reassembled on the way out.
//
// Values larger than a chunk are not arena-backed: they get a standalone copy, so the
// reusable chunks stay a uniform size and one huge value cannot pin an oversized chunk for
// the arena's lifetime.
func (a *Arena) Append(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	a.size += len(b)
	if len(b) > a.chunkSize() {
		return bytes.Clone(b)
	}
	// Skip chunks that lack room; never grow an existing chunk in place (that could
	// reallocate and invalidate views already handed out from it).
	for a.idx < len(a.chunks) && cap(a.chunks[a.idx])-len(a.chunks[a.idx]) < len(b) {
		a.idx++
	}
	if a.idx == len(a.chunks) {
		a.chunks = append(a.chunks, make([]byte, 0, a.chunkSize()))
	}
	cur := a.chunks[a.idx]
	start := len(cur)
	cur = append(cur, b...) // in-cap append: same backing array, no reallocation
	a.chunks[a.idx] = cur
	return cur[start:]
}

// Ref locates a value in the arena WITHOUT a pointer to it — a chunk index and a range
// within that chunk. Value resolves it.
//
// The pointer-free part is the point, and it is worth the extra indirection on every read.
// A []Ref is allocated noscan, so the GC never walks it and a sort never pays a write
// barrier moving one; the []byte form that Append returns puts a pointer in every element,
// and a caller holding millions of them turns its largest structure into scan work on every
// cycle. Measured on a production ingest service: the []byte form put
// runtime.gcBgMarkWorker at 11.1% of the process against 0.5% elsewhere in the tree. It is
// also half the width, 12 bytes against 24, which for a caller that retains descriptors is
// the same saving twice over.
//
// The zero Ref is the ABSENT value: a stored value is never zero bytes long (AppendRef
// returns the zero Ref for empty input), so end <= off cannot name a real one.
//
// int32 bounds both fields well below 2^31 — the chunk index by the arena's total size, the
// offsets by the chunk size, except in an oversized chunk where they are bounded by the
// single value that made it.
type Ref struct {
	chunk    int32
	off, end int32
}

// Empty reports whether r names no value — the absent/null descriptor.
func (r Ref) Empty() bool { return r.end <= r.off }

// AppendRef copies b into the arena and returns a pointer-free Ref to the copy, for a caller
// that keeps many descriptors and does not want them scanned (see Ref).
//
// Unlike Append, an oversized value goes into a chunk of its own rather than a standalone
// copy, because a Ref can only address chunk storage. That chunk is NOT uniform, so it is
// kept by Reset and only freed by Release — fine for a caller that releases per batch, worth
// knowing for one that does not.
func (a *Arena) AppendRef(b []byte) Ref {
	if len(b) == 0 {
		return Ref{}
	}
	a.size += len(b)
	for a.idx < len(a.chunks) && cap(a.chunks[a.idx])-len(a.chunks[a.idx]) < len(b) {
		a.idx++
	}
	if a.idx == len(a.chunks) {
		a.chunks = append(a.chunks, make([]byte, 0, max(len(b), a.chunkSize())))
	}
	cur := a.chunks[a.idx]
	start := len(cur)
	a.chunks[a.idx] = append(cur, b...)
	return Ref{chunk: int32(a.idx), off: int32(start), end: int32(start + len(b))}
}

// Value resolves r against the arena, or nil for the absent descriptor.
func (a *Arena) Value(r Ref) []byte {
	if r.Empty() {
		return nil
	}
	return a.chunks[r.chunk][r.off:r.end]
}

// Size reports the bytes stored since the last Reset or Release.
func (a *Arena) Size() int { return a.size }

// Intern copies s into the arena and returns a stable string view over the copy.
// Strings larger than a chunk are not arena-backed: they get a standalone copy so
// the arena's reusable chunks stay a uniform size and a single huge string cannot
// pin an oversized chunk for the arena's lifetime.
func (a *Arena) Intern(s string) string {
	if len(s) == 0 {
		return ""
	}
	if len(s) > a.chunkSize() {
		return strings.Clone(s) // a standalone copy, already immutable: no b2s aliasing concern
	}
	return b2s(a.Append(unsafe.Slice(unsafe.StringData(s), len(s))))
}

// Reset rewinds the arena so its chunks are reused for the next round. The caller
// must ensure every string previously interned is dead before calling Reset.
func (a *Arena) Reset() {
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
func (a *Arena) Release() {
	a.chunks = nil
	a.idx = 0
	a.size = 0
}

// Retained reports the bytes the arena keeps across Resets — its chunk capacity.
// Chunks are uniform (oversized strings get standalone clones that die with their
// batch), so this is exact, and it is the footprint a trim policy should judge (a burst
// grows the chunk list; Reset rewinds but never shrinks it).
func (a *Arena) Retained() int {
	// Summed rather than len(chunks)*chunkSize: AppendRef gives an oversized value a chunk
	// of its own, so the chunks are not always uniform and the multiplication would lie.
	n := 0
	for _, c := range a.chunks {
		n += cap(c)
	}
	return n
}
