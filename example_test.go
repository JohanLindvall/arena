package arena_test

import (
	"fmt"

	"github.com/JohanLindvall/arena"
)

// A batch-lifetime workload: intern the batch's values, use the views, then rewind
// the arena and run the next batch over the same chunks.
func Example() {
	var a arena.StringArena // the zero value is usable, with 64 KiB chunks

	for batch := range 2 {
		labels := make([]string, 0, 3)
		for _, key := range []string{"container_id", "level", "namespace"} {
			// A copy, so whatever the decoder underneath reuses its buffer for
			// next cannot show through.
			labels = append(labels, a.Intern(key))
		}
		fmt.Printf("batch %d: %v, %d bytes interned\n", batch, labels, a.Size())

		// Every view above is dead from here, so the chunks can be handed out again.
		a.Reset()
	}
	// Output:
	// batch 0: [container_id level namespace], 26 bytes interned
	// batch 1: [container_id level namespace], 26 bytes interned
}

// Chunk size decides the tail waste, since a value that does not fit the current
// chunk starts a new one and strands the remainder. Size it to the values.
func ExampleNew() {
	a := arena.New[byte](1 << 20) // 1 MiB, for values that would shred a 64 KiB chunk

	row := a.Append([]byte("a row too long to pack well into a small chunk"))
	fmt.Printf("%s (%d bytes retained)\n", row, a.Retained())
	// Output: a row too long to pack well into a small chunk (1048576 bytes retained)
}

// An interned view stays valid until Reset no matter how much is interned after it:
// a value that does not fit the current chunk starts a new chunk rather than growing
// one that already has views pointing into it.
func ExampleStringArena_Intern() {
	a := arena.NewStringArena(4096)

	first := a.Intern("still here")
	for range 1000 {
		a.Intern("filler, enough of it to force several new chunks")
	}

	fmt.Println(first, "-", a.Retained() > 4096, "chunks were added after it")
	// Output: still here - true chunks were added after it
}

// Ref is the descriptor form: a chunk index and a range, with no pointer in it. A
// []Ref is allocated noscan, so a caller holding millions of them across a batch
// costs the collector nothing, where the []byte Append returns would put a pointer
// in every element and turn that slice into scan work on every cycle.
func ExampleArena_AppendRef() {
	var a arena.Arena[byte]

	refs := make([]arena.Ref[byte], 0, 3)
	for _, line := range []string{`{"n":1}`, `{"n":2}`, ""} {
		refs = append(refs, a.AppendRef([]byte(line)))
	}

	for _, r := range refs {
		if r.Empty() { // the zero Ref, which is what empty input returns
			fmt.Println("(absent)")
			continue
		}
		fmt.Printf("%s\n", a.Value(r))
	}
	// Output:
	// {"n":1}
	// {"n":2}
	// (absent)
}

// Reset and Release are both all-at-once rewinds, and differ only in what they keep.
func ExampleArena_Release() {
	a := arena.New[byte](4096)
	a.Append(make([]byte, 3000))

	// Reset drops the bytes and keeps the chunks, for an owner about to refill them.
	a.Reset()
	fmt.Println("after Reset:", a.Size(), "bytes stored,", a.Retained(), "retained")

	// Release drops the chunks too, for an owner that outlives its bytes — a pooled
	// writer parked empty between batches.
	a.Release()
	fmt.Println("after Release:", a.Size(), "bytes stored,", a.Retained(), "retained")
	// Output:
	// after Reset: 0 bytes stored, 4096 retained
	// after Release: 0 bytes stored, 0 retained
}

// The element type is not limited to bytes. An arena over any T stores slices of T and
// hands back views over the copies, with the same one-batch lifetime.
func ExampleArena() {
	type sample struct {
		at    int64
		value float64
	}

	// The chunk size is a byte budget, divided down to a whole number of elements — so a
	// wider T means fewer of them per chunk, not a bigger chunk.
	a := arena.New[sample](4096) // 4 KiB, which is 256 samples

	window := a.Append([]sample{{at: 1, value: 0.5}, {at: 2, value: 1.5}})
	fmt.Println(window, "-", a.Size(), "bytes stored,", a.Retained(), "retained")
	// Output: [{1 0.5} {2 1.5}] - 32 bytes stored, 4096 retained
}

// StrRef and Str are the descriptor form of Intern: the arena still owns the bytes, but
// what the caller retains is 12 pointer-free bytes rather than a string header.
func ExampleStringArena_StrRef() {
	a := arena.NewStringArena(4096)

	refs := make([]arena.Ref[byte], 0, 3)
	for _, s := range []string{"container_id", "level", ""} {
		refs = append(refs, a.StrRef(s))
	}

	for _, r := range refs {
		fmt.Printf("%q empty=%v\n", a.Str(r), r.Empty())
	}
	// Output:
	// "container_id" empty=false
	// "level" empty=false
	// "" empty=true
}

// Reserve is Append without the copy, for a value that is being built rather than one the
// caller already has. A decoder that learns an array's length before its elements reserves
// the backing once and fills it in place — no allocation per value, and no copy out of a
// scratch buffer either.
func ExampleArena_Reserve() {
	a := arena.New[float64](4096)

	// Three points, each backed by the same chunk rather than three make calls.
	var points [][]float64
	for _, row := range [][]float64{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}} {
		p := a.Reserve(len(row)) // length 0, capacity exactly len(row)
		for _, v := range row {
			p = append(p, v*10)
		}
		points = append(points, p)
	}

	fmt.Println(points, "-", a.Size(), "bytes stored,", a.Retained(), "retained")
	// Output: [[10 20 30] [40 50 60] [70 80 90]] - 72 bytes stored, 4096 retained
}

// Make is New without the allocation, for an arena that lives inside something the caller
// already has. The zero Arena needs no constructor at all; Make is how a struct field
// picks a chunk size other than the 64 KiB default.
func ExampleMake() {
	type decoder struct {
		coords arena.Arena[float64]
		counts arena.Arena[int32]
	}
	d := decoder{
		coords: arena.Make[float64](4096),
		counts: arena.Make[int32](4096),
	}

	xs := d.coords.Append([]float64{0.5, 1.5})
	ns := d.counts.Append([]int32{7})
	fmt.Println(xs, ns, "-", d.coords.Retained()+d.counts.Retained(), "bytes retained")
	// Output: [0.5 1.5] [7] - 8192 bytes retained
}
