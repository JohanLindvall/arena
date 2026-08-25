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
	var a Arena
	for _, s := range []string{"", "a", "container_id", "level"} {
		if got := a.Intern(s); got != s {
			t.Errorf("Intern(%q) = %q, want %q", s, got, s)
		}
	}
}

// TestArenaInternCopies verifies an interned view is a copy: mutating the source
// bytes after interning must not change the returned string.
func TestArenaInternCopies(t *testing.T) {
	var a Arena
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
// defaultChunkSize and that strings interned earlier stay byte-identical after later
// interns force new chunks (i.e. no chunk is ever reallocated under a live view).
// It also checks that Reset lets the arena be reused.
func TestArenaOversizeAndStability(t *testing.T) {
	var a Arena

	// A string well past a single chunk must round-trip intact.
	big := strings.Repeat("x", defaultChunkSize*2+123)
	if got := a.Intern(big); got != big {
		t.Fatalf("oversize Intern round-trip failed (len got %d want %d)", len(got), len(big))
	}

	// Intern many strings (small and oversized) and keep every returned view; once
	// all are interned, each must still equal its source. If any chunk had been
	// reallocated to grow, an earlier view would now read garbage.
	inputs := make([]string, 0, 5004)
	inputs = append(inputs, "", "a", "container_id", strings.Repeat("y", defaultChunkSize+1))
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
	var a Arena
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
	a := New(defaultChunkSize)
	var want []string
	var refs [][]byte
	add := func(s string) {
		want = append(want, s)
		refs = append(refs, a.Append([]byte(s)))
	}
	for i := range 300 {
		add(fmt.Sprintf(`{"i":%d,"pad":%q}`, i, strings.Repeat("y", 400)))
	}
	add(`"` + strings.Repeat("z", 3*defaultChunkSize) + `"`) // larger than a whole chunk
	add(`1`)

	require.Greater(t, a.Retained(), defaultChunkSize, "the fixture must span more than one chunk")
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

// Test_unit_Arena_OversizedValueIsStandalone: a value larger than a chunk gets its own copy
// rather than an oversized chunk, so the reusable chunks stay a uniform size and one huge
// value cannot pin memory for the arena's lifetime. Retained counts only the uniform chunks,
// which is what makes it exact for a trim policy.
func Test_unit_Arena_OversizedValueIsStandalone(t *testing.T) {
	a := New(defaultChunkSize)
	a.Append([]byte(`1`))
	huge := []byte(`"` + strings.Repeat("z", 2*defaultChunkSize) + `"`)
	ref := a.Append(huge)

	assert.Equal(t, string(huge), string(ref), "an oversized value must still read back whole")
	assert.Equal(t, defaultChunkSize, a.Retained(), "the oversized value must not become a retained chunk")

	// It still counts as stored bytes, because callers bound a payload with Size.
	assert.Equal(t, 1+len(huge), a.Size())
}

// Test_unit_Arena_ReleaseDropsChunksResetKeepsThem pins the difference between the two
// rewinds. Reset is for the next batch of the same owner and keeps the chunks, because
// re-allocating them is the largest allocation the owner would make; Release is for an owner
// that outlives its bytes — a pooled writer — and must actually free them.
func Test_unit_Arena_ReleaseDropsChunksResetKeepsThem(t *testing.T) {
	a := New(defaultChunkSize)
	for range 100 {
		a.Append([]byte(strings.Repeat("x", 4000)))
	}
	grown := a.Retained()
	require.Greater(t, grown, defaultChunkSize)

	a.Reset()
	assert.Zero(t, a.Size())
	assert.Equal(t, grown, a.Retained(), "Reset must keep the chunks for reuse")

	a.Release()
	assert.Zero(t, a.Size())
	assert.Zero(t, a.Retained(), "Release must actually drop the memory")
}

func Test_unit_Arena_Retained(t *testing.T) {
	var a Arena
	if got := a.Retained(); got != 0 {
		t.Fatalf("fresh arena retains %d", got)
	}
	// Two chunk-filling strings -> two chunks; Reset rewinds but never shrinks —
	// exactly the ratchet a trim policy has to judge.
	a.Intern(string(make([]byte, defaultChunkSize)))
	a.Intern(string(make([]byte, defaultChunkSize)))
	two := a.Retained()
	if two != 2*defaultChunkSize {
		t.Fatalf("retained %d, want %d", two, 2*defaultChunkSize)
	}
	a.Reset()
	if got := a.Retained(); got != two {
		t.Fatalf("Reset changed retained from %d to %d", two, got)
	}
	// Oversized strings are standalone clones, not chunks: retained is unchanged.
	a.Intern(string(make([]byte, defaultChunkSize+1)))
	if got := a.Retained(); got != two {
		t.Fatalf("oversized intern changed retained from %d to %d", two, got)
	}
}

// Test_unit_Arena_RefIsPointerFreeAndResolves covers the descriptor form: it must read back
// whole across a chunk boundary and past it, and it must contain no pointer — that is the
// whole reason it exists over the []byte Append returns, since a caller retaining millions
// of them would otherwise turn its largest structure into GC scan work.
func Test_unit_Arena_RefIsPointerFreeAndResolves(t *testing.T) {
	a := New(1 << 12)
	want := make([]string, 0, 201)
	refs := make([]Ref, 0, 201)
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
	assert.Equal(t, 12, int(unsafe.Sizeof(Ref{})), "a Ref must stay 12 bytes")

	assert.True(t, Ref{}.Empty(), "the zero Ref is the absent value")
	assert.Nil(t, a.Value(Ref{}), "resolving the absent value must not index a chunk")
	assert.True(t, a.AppendRef(nil).Empty(), "an empty append is the absent value")
	assert.False(t, refs[0].Empty())

	// Release frees the oversized chunk too, which is what makes it safe for a per-batch
	// caller: Reset alone would keep it, since it is not a uniform chunk.
	a.Release()
	assert.Zero(t, a.Retained())
}
