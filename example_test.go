package arena_test

import (
	"fmt"

	"github.com/JohanLindvall/arena"
)

// A batch-lifetime workload: intern the batch's values, use the views, then rewind
// the arena and run the next batch over the same chunks.
func Example() {
	var a arena.Arena // the zero value is usable, with 64 KiB chunks

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
	a := arena.New(1 << 20) // 1 MiB, for values that would shred a 64 KiB chunk

	row := a.Append([]byte("a row too long to pack well into a small chunk"))
	fmt.Printf("%s (%d bytes retained)\n", row, a.Retained())
	// Output: a row too long to pack well into a small chunk (1048576 bytes retained)
}

// An interned view stays valid until Reset no matter how much is interned after it:
// a value that does not fit the current chunk starts a new chunk rather than growing
// one that already has views pointing into it.
func ExampleArena_Intern() {
	a := arena.New(4096)

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
	var a arena.Arena

	refs := make([]arena.Ref, 0, 3)
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
	a := arena.New(4096)
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
