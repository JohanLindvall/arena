package arena

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestArenaInternBasic checks that Intern round-trips small strings and that the
// empty string is never chunk-backed.
func TestArenaInternBasic(t *testing.T) {
	var a StringArena
	for _, s := range []string{"", "a", "container_id", "level"} {
		if got := a.Intern(s); got != s {
			t.Errorf("Intern(%q) = %q, want %q", s, got, s)
		}
	}
}

// TestArenaInternCopies verifies an interned view is a copy: mutating the source
// bytes after interning must not change the returned string.
func TestArenaInternCopies(t *testing.T) {
	var a StringArena
	src := []byte("hello")
	got := a.Intern(string(src))
	for i := range src {
		src[i] = 'x'
	}
	if got != "hello" {
		t.Errorf("interned value changed with source mutation: got %q", got)
	}
}

// TestArenaOversizeAndStability verifies the arena handles strings longer than
// defaultChunkBytes and that strings interned earlier stay byte-identical after later
// interns force new chunks (i.e. no chunk is ever reallocated under a live view).
// It also checks that Reset lets the arena be reused.
func TestArenaOversizeAndStability(t *testing.T) {
	var a StringArena

	// A string well past a single chunk must round-trip intact.
	big := strings.Repeat("x", defaultChunkBytes*2+123)
	if got := a.Intern(big); got != big {
		t.Fatalf("oversize Intern round-trip failed (len got %d want %d)", len(got), len(big))
	}

	// Intern many strings (small and oversized) and keep every returned view; once
	// all are interned, each must still equal its source. If any chunk had been
	// reallocated to grow, an earlier view would now read garbage.
	inputs := make([]string, 0, 5004)
	inputs = append(inputs, "", "a", "container_id", strings.Repeat("y", defaultChunkBytes+1))
	for i := range 5000 {
		inputs = append(inputs, strings.Repeat("z", i%200))
	}
	got := make([]string, len(inputs))
	for i, s := range inputs {
		got[i] = a.Intern(s)
	}
	for i, s := range inputs {
		if got[i] != s {
			t.Errorf("interned string %d changed (len got %d want %d)", i, len(got[i]), len(s))
		}
	}
	// The empty string is never backed by a chunk.
	if got := a.Intern(""); got != "" {
		t.Errorf(`Intern("") = %q, want ""`, got)
	}

	// After Reset the arena is reusable and still correct (chunks are rewound, not
	// dropped).
	a.Reset()
	if got := a.Intern(big); got != big {
		t.Errorf("after Reset, oversize Intern round-trip failed")
	}
	if got := a.Intern("hello"); got != "hello" {
		t.Errorf("after Reset, Intern(hello) = %q", got)
	}
}

// TestArenaResetReusesChunks verifies Reset rewinds rather than drops the backing:
// the chunk count does not grow when re-interning the same workload after Reset.
func TestArenaResetReusesChunks(t *testing.T) {
	var a StringArena
	fill := func() {
		for range 2000 {
			a.Intern(strings.Repeat("a", 100))
		}
	}
	fill()
	chunksAfterFirst := len(a.chunks)
	a.Reset()
	fill()
	if len(a.chunks) != chunksAfterFirst {
		t.Errorf("Reset did not reuse chunks: had %d, grew to %d", chunksAfterFirst, len(a.chunks))
	}
}

// Test_unit_Arena_AppendValuesStayContiguous: a caller that reads values back through the
// slices it was handed depends on each one being ONE slice — an emit loop writes each value
// to the compressor in a single call. This walks a fixture over a chunk boundary
// and past it, including a value larger than a whole chunk, and reads every one back.
func Test_unit_Arena_AppendValuesStayContiguous(t *testing.T) {
	a := New[byte](defaultChunkBytes)
	var want []string
	var refs [][]byte
	add := func(s string) {
		want = append(want, s)
		refs = append(refs, a.Append([]byte(s)))
	}
	for i := range 300 {
		add(fmt.Sprintf(`{"i":%d,"pad":%q}`, i, strings.Repeat("y", 400)))
	}
	add(`"` + strings.Repeat("z", 3*defaultChunkBytes) + `"`) // larger than a whole chunk
	add(`1`)

	require.Greater(t, a.Retained(), defaultChunkBytes, "the fixture must span more than one chunk")
	total := 0
	for i, ref := range refs {
		assert.Equal(t, want[i], string(ref), "value %d must read back whole", i)
		total += len(want[i])
	}
	assert.Equal(t, total, a.Size(), "Size must be the exact payload length, oversized copies included")

	// Empty input stores nothing and hands back a nil view, mirroring the zero Ref
	// that AppendRef returns for it.
	assert.Nil(t, a.Append(nil), "an empty append must store nothing")
	assert.Equal(t, total, a.Size(), "an empty append must not move Size")
}

// Test_unit_Arena_OversizedValueGetsItsOwnChunk: a value larger than a chunk is still
// arena-backed, in a chunk sized to fit it exactly. What stops that pinning memory is
// Reset, which drops such a chunk rather than recycling it — so what carries over to the
// next batch is uniform however lumpy the batch that just ran was.
func Test_unit_Arena_OversizedValueGetsItsOwnChunk(t *testing.T) {
	a := New[byte](defaultChunkBytes)
	a.Append([]byte(`1`))
	huge := []byte(`"` + strings.Repeat("z", 2*defaultChunkBytes) + `"`)
	view := a.Append(huge)

	assert.Equal(t, string(huge), string(view), "an oversized value must still read back whole")
	assert.Equal(t, defaultChunkBytes+len(huge), a.Retained(),
		"the oversized value must get a chunk of its own, sized to fit it exactly")

	// It counts as stored bytes either way, because callers bound a payload with Size.
	assert.Equal(t, 1+len(huge), a.Size())

	// The uniform chunk is rewound and kept; the oversized one is dropped outright.
	a.Reset()
	assert.Zero(t, a.Size())
	assert.Equal(t, defaultChunkBytes, a.Retained(), "Reset must not recycle the oversized chunk")
}

// Test_unit_Arena_ReleaseDropsChunksResetKeepsThem pins the difference between the two
// rewinds. Reset is for the next batch of the same owner and keeps the chunks, because
// re-allocating them is the largest allocation the owner would make; Release is for an owner
// that outlives its bytes — a pooled writer — and must actually free them.
func Test_unit_Arena_ReleaseDropsChunksResetKeepsThem(t *testing.T) {
	a := New[byte](defaultChunkBytes)
	for range 100 {
		a.Append([]byte(strings.Repeat("x", 4000)))
	}
	grown := a.Retained()
	require.Greater(t, grown, defaultChunkBytes)

	a.Reset()
	assert.Zero(t, a.Size())
	assert.Equal(t, grown, a.Retained(), "Reset must keep the chunks for reuse")

	a.Release()
	assert.Zero(t, a.Size())
	assert.Zero(t, a.Retained(), "Release must actually drop the memory")
}

func Test_unit_Arena_Retained(t *testing.T) {
	var a StringArena
	if got := a.Retained(); got != 0 {
		t.Fatalf("fresh arena retains %d", got)
	}
	// Two chunk-filling strings -> two chunks; Reset rewinds but never shrinks —
	// exactly the ratchet a trim policy has to judge.
	a.Intern(string(make([]byte, defaultChunkBytes)))
	a.Intern(string(make([]byte, defaultChunkBytes)))
	two := a.Retained()
	if two != 2*defaultChunkBytes {
		t.Fatalf("retained %d, want %d", two, 2*defaultChunkBytes)
	}
	a.Reset()
	if got := a.Retained(); got != two {
		t.Fatalf("Reset changed retained from %d to %d", two, got)
	}
	// An oversized string is a chunk of its own, so it counts while it is in flight — and
	// stops counting at the Reset that drops it rather than recycling it.
	oversize := defaultChunkBytes + 1
	a.Intern(string(make([]byte, oversize)))
	if got := a.Retained(); got != two+oversize {
		t.Fatalf("oversized intern: retained %d, want %d", got, two+oversize)
	}
	a.Reset()
	if got := a.Retained(); got != two {
		t.Fatalf("Reset recycled the oversized chunk: retained %d, want %d", got, two)
	}
}

// Test_unit_Arena_RefIsPointerFreeAndResolves covers the descriptor form: it must read back
// whole across a chunk boundary and past it, and it must contain no pointer — that is the
// whole reason it exists over the []byte Append returns, since a caller retaining millions
// of them would otherwise turn its largest structure into GC scan work.
func Test_unit_Arena_RefIsPointerFreeAndResolves(t *testing.T) {
	a := New[byte](1 << 12)
	want := make([]string, 0, 201)
	refs := make([]Ref[byte], 0, 201)
	for i := range 200 {
		s := fmt.Sprintf(`{"i":%d,"pad":%q}`, i, strings.Repeat("y", 100))
		want = append(want, s)
		refs = append(refs, a.AppendRef([]byte(s)))
	}
	// An oversized value goes into a chunk of its own, because a Ref can only address chunks.
	big := strings.Repeat("z", 3*(1<<12))
	want = append(want, big)
	refs = append(refs, a.AppendRef([]byte(big)))

	require.Greater(t, a.Retained(), 1<<12, "the fixture must span more than one chunk")
	for i, r := range refs {
		assert.Equal(t, want[i], string(a.Value(r)), "value %d must read back whole", i)
	}

	// Pointer-free: reflect sees three int32s and nothing to scan.
	assert.Equal(t, 12, int(unsafe.Sizeof(Ref[byte]{})), "a Ref must stay 12 bytes")

	assert.True(t, Ref[byte]{}.Empty(), "the zero Ref is the absent value")
	assert.Nil(t, a.Value(Ref[byte]{}), "resolving the absent value must not index a chunk")
	assert.True(t, a.AppendRef(nil).Empty(), "an empty append is the absent value")
	assert.False(t, refs[0].Empty())

	// Release drops everything, the uniform chunks included — those a Reset would keep.
	a.Release()
	assert.Zero(t, a.Retained())
}

// Test_unit_Arena_GenericElementType covers an element type that is not byte, which is
// where the byte/element distinction has teeth: New takes a byte budget and has to divide
// it down to a count of T, while Size and Retained have to scale back up. A T with a
// pointer in it also has to survive the round trip, since the chunks are ordinary Go
// slices and are what keeps the pointee alive.
func Test_unit_Arena_GenericElementType(t *testing.T) {
	type row struct {
		id   int64
		name string // a pointer, so these chunks are scanned rather than noscan
	}
	const width = int(unsafe.Sizeof(row{}))
	const perChunk = 16

	a := New[row](perChunk * width)

	want := make([][]row, 0, 40)
	refs := make([]Ref[row], 0, 40)
	for i := range 40 {
		pair := []row{{id: int64(i), name: fmt.Sprintf("row-%d", i)}, {id: int64(-i), name: "second"}}
		want = append(want, pair)
		refs = append(refs, a.AppendRef(pair))
	}

	require.Greater(t, a.Retained(), perChunk*width, "the fixture must span more than one chunk")
	for i, r := range refs {
		assert.Equal(t, want[i], a.Value(r), "value %d must read back whole", i)
	}

	// Both counters are bytes, not elements.
	assert.Equal(t, 80*width, a.Size(), "Size must scale the element count by the width of T")
	assert.Zero(t, a.Retained()%(perChunk*width), "every chunk must be a whole number of rows wide")

	// A Ref stays 12 bytes and pointer-free even when T is not — that is the whole point
	// of it being a chunk index and a range rather than a slice header.
	assert.Equal(t, 12, int(unsafe.Sizeof(Ref[row]{})), "a Ref must stay 12 bytes for any T")

	a.Release()
	assert.Zero(t, a.Retained())
}

// Test_unit_Arena_ChunkBudgetIsBytes pins the sizing rule that the byte budget implies:
// a wider T means fewer elements to the chunk, never a bigger chunk. Sizing in elements
// instead would quietly hand an arena over a 64-byte struct 4 MiB chunks.
func Test_unit_Arena_ChunkBudgetIsBytes(t *testing.T) {
	// The zero value defaults to the same budget whatever T is.
	var wide Arena[int64]
	wide.Append([]int64{1})
	assert.Equal(t, defaultChunkBytes, wide.Retained(), "the zero value is 64 KiB, not 64 Ki elements")

	// An explicit budget rounds down to a whole number of elements: 1000 bytes is 125
	// int64s, and the 0 bytes left over are not a chunk.
	rounded := New[int64](1000)
	rounded.Append([]int64{1})
	assert.Equal(t, 125*8, rounded.Retained())

	// A budget smaller than one element still holds one, rather than a chunk of nothing.
	tiny := New[int64](3)
	tiny.Append([]int64{1})
	assert.Equal(t, 8, tiny.Retained())

	// A zero-width element type must not divide by zero on the way to a count.
	empty := New[struct{}](4096)
	assert.Len(t, empty.Value(empty.AppendRef(make([]struct{}, 3))), 3)
	assert.Zero(t, empty.Size(), "zero-width elements occupy no bytes")
}

// Test_unit_StringArena_StrRefAndStr covers the descriptor path for strings. A caller that
// retains a great many of them wants Ref[byte] rather than string headers, so StrRef has to
// round-trip through Str across a chunk boundary, past a whole chunk, and for the empty
// string — and has to interoperate with the byte side, since it is the same descriptor.
func Test_unit_StringArena_StrRefAndStr(t *testing.T) {
	const chunk = 1 << 12
	a := NewStringArena(chunk)

	want := make([]string, 0, 202)
	refs := make([]Ref[byte], 0, 202)
	add := func(s string) {
		want = append(want, s)
		refs = append(refs, a.StrRef(s))
	}
	for i := range 200 {
		add(fmt.Sprintf("label-%d-%s", i, strings.Repeat("y", 100)))
	}
	// An oversized string is chunk-backed like any other, so a Ref addresses it fine.
	add(strings.Repeat("z", 3*chunk))
	add("")

	require.Greater(t, a.Retained(), chunk, "the fixture must span more than one chunk")
	// Checked after every StrRef, so a descriptor taken early must survive the chunks
	// added behind it.
	for i, r := range refs {
		assert.Equal(t, want[i], a.Str(r), "value %d must read back whole", i)
	}

	assert.True(t, a.StrRef("").Empty(), "the empty string is the absent descriptor")
	assert.Equal(t, "", a.Str(Ref[byte]{}), "the absent descriptor resolves to the empty string")

	// StrRef hands out the same descriptor AppendRef does, so the two sides interoperate.
	assert.Equal(t, "raw", a.Str(a.AppendRef([]byte("raw"))))
	assert.Equal(t, []byte("via StrRef"), a.Value(a.StrRef("via StrRef")))
}

// Test_unit_Arena_ResetDropsOversizedChunks is the boundary between the two rewinds. A
// batch that stored values no chunk could hold must hand exactly their memory back at
// Reset while keeping the uniform chunks it is about to refill, so a lumpy batch cannot
// ratchet the arena's footprint up for good.
func Test_unit_Arena_ResetDropsOversizedChunks(t *testing.T) {
	const chunk = 1 << 12
	a := New[byte](chunk)

	for range 3 * chunk {
		a.Append([]byte("x"))
	}
	uniform := a.Retained()
	require.Equal(t, 3*chunk, uniform, "the small values must fill whole uniform chunks")

	// Both entry points take the same path, so both leave a chunk sized to the value.
	huge := make([]byte, 5*chunk)
	for range 4 {
		a.AppendRef(huge)
		a.Append(huge)
	}
	assert.Equal(t, uniform+8*len(huge), a.Retained(),
		"Append and AppendRef must both give an oversized value a chunk sized to fit")

	a.Reset()
	assert.Equal(t, uniform, a.Retained(), "Reset keeps the uniform chunks and drops the rest")

	// And the arena is immediately reusable, refilling what it kept rather than growing.
	for range 3 * chunk {
		a.Append([]byte("y"))
	}
	assert.Equal(t, uniform, a.Retained(), "the next batch must refill the chunks Reset kept")
}
