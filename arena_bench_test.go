package arena

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// batch is a batch's worth of values between rewinds. Benchmarks that measure the arena
// carry their share of a Reset rather than pretending it grows forever, and benchmarks
// that compare against the heap retain the same number of live values on both sides —
// a value that dies immediately would be stack-allocated and prove nothing.
const batch = 4096

// BenchmarkIntern measures the arena against the obvious alternative, one heap-allocated
// copy per value, with both sides holding a batch live. The arena spends one allocation
// per chunk where the baseline spends one per value, so allocs/op is as much the point
// as ns/op.
func BenchmarkIntern(b *testing.B) {
	for _, size := range []int{16, 256, 4096} {
		value := strings.Repeat("x", size)

		b.Run(fmt.Sprintf("arena/%d", size), func(b *testing.B) {
			a := NewStringArena(defaultChunkBytes)
			kept := make([]string, 0, batch)
			b.ReportAllocs()
			for b.Loop() {
				if len(kept) == batch {
					kept = kept[:0] // every view is dead here, so the chunks can be reused
					a.Reset()
				}
				kept = append(kept, a.Intern(value))
			}
			runtime.KeepAlive(kept)
		})

		b.Run(fmt.Sprintf("clone/%d", size), func(b *testing.B) {
			kept := make([]string, 0, batch)
			b.ReportAllocs()
			for b.Loop() {
				if len(kept) == batch {
					kept = kept[:0] // the copies become garbage instead
				}
				kept = append(kept, strings.Clone(value))
			}
			runtime.KeepAlive(kept)
		})
	}
}

// BenchmarkAppend covers the byte primitive across sizes, including the oversized path
// where a value larger than a chunk falls back to a standalone copy and the arena buys
// nothing at all.
func BenchmarkAppend(b *testing.B) {
	for _, size := range []int{16, 256, 4096, defaultChunkBytes + 1} {
		value := make([]byte, size)

		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			a := New[byte](defaultChunkBytes)
			b.ReportAllocs()
			n := 0
			for b.Loop() {
				if n == batch {
					a.Reset()
					n = 0
				}
				_ = a.Append(value)
				n++
			}
		})
	}
}

// BenchmarkAppendRef compares the two store paths. They should land close together: the
// point of Ref is not that storing is faster but that a caller retaining the descriptors
// is cheaper to collect, which is what BenchmarkRetainedDescriptorGC measures.
func BenchmarkAppendRef(b *testing.B) {
	value := make([]byte, 256)

	b.Run("Append", func(b *testing.B) {
		a := New[byte](defaultChunkBytes)
		b.ReportAllocs()
		n := 0
		for b.Loop() {
			if n == batch {
				a.Reset()
				n = 0
			}
			_ = a.Append(value)
			n++
		}
	})

	b.Run("AppendRef", func(b *testing.B) {
		a := New[byte](defaultChunkBytes)
		b.ReportAllocs()
		n := 0
		for b.Loop() {
			if n == batch {
				a.Reset()
				n = 0
			}
			_ = a.AppendRef(value)
			n++
		}
	})
}

// BenchmarkRetainedDescriptorGC is the reason Ref exists. It holds a batch's worth of
// descriptors live and times a full GC cycle over each form: []byte puts a pointer in
// every element, so the collector walks the whole slice on every cycle, where []Ref is
// allocated noscan and is skipped outright.
//
// Both variants retain the same arena bytes. Only the descriptor slice differs.
func BenchmarkRetainedDescriptorGC(b *testing.B) {
	const n = 1 << 20 // descriptors a large batch might carry
	value := []byte(`{"ts":1755000000,"level":"info"}`)

	b.Run("Ref", func(b *testing.B) {
		a := New[byte](defaultChunkBytes)
		refs := make([]Ref[byte], n)
		for i := range refs {
			refs[i] = a.AppendRef(value)
		}
		for b.Loop() {
			runtime.GC()
		}
		runtime.KeepAlive(refs) // declared outside the loop, so keep it live by hand
		runtime.KeepAlive(a)
	})

	b.Run("bytes", func(b *testing.B) {
		a := New[byte](defaultChunkBytes)
		views := make([][]byte, n)
		for i := range views {
			views[i] = a.Append(value)
		}
		for b.Loop() {
			runtime.GC()
		}
		runtime.KeepAlive(views)
		runtime.KeepAlive(a)
	})

	b.Run("strings", func(b *testing.B) {
		a := NewStringArena(defaultChunkBytes)
		views := make([]string, n)
		for i := range views {
			views[i] = a.Intern(string(value))
		}
		for b.Loop() {
			runtime.GC()
		}
		runtime.KeepAlive(views)
		runtime.KeepAlive(a)
	})
}

// BenchmarkBatchCycle is the whole shape a caller actually runs: fill a batch, read it
// back, rewind. The rewind is as much the subject as the filling — Reset is what makes
// the next batch's chunks free, and Release is what it costs to give them back.
func BenchmarkBatchCycle(b *testing.B) {
	values := make([][]byte, 512)
	for i := range values {
		values[i] = fmt.Appendf(nil, `{"i":%d,"msg":%q}`, i, strings.Repeat("y", 120))
	}

	for _, rewind := range []string{"Reset", "Release"} {
		release := rewind == "Release" // hoisted: not part of what is being measured
		b.Run(rewind, func(b *testing.B) {
			a := New[byte](defaultChunkBytes)
			refs := make([]Ref[byte], 0, len(values))
			b.ReportAllocs()
			for b.Loop() {
				refs = refs[:0]
				for _, v := range values {
					refs = append(refs, a.AppendRef(v))
				}
				total := 0
				for _, r := range refs {
					total += len(a.Value(r))
				}
				if release {
					a.Release()
				} else {
					a.Reset()
				}
			}
		})
	}
}

// BenchmarkElementWidth stores the same number of bytes through arenas of different
// element types. The code per element is identical; what the width changes is how many
// elements a chunk holds, since the chunk budget is bytes. The struct case also carries a
// pointer, so its chunks are scanned where the other two are noscan.
func BenchmarkElementWidth(b *testing.B) {
	type row struct {
		id    int64
		score float64
		name  string
	}

	b.Run("byte", func(b *testing.B) {
		v := make([]byte, 32*8)
		a := New[byte](defaultChunkBytes)
		benchStore(b, a, v)
	})
	b.Run("int64", func(b *testing.B) {
		v := make([]int64, 32)
		a := New[int64](defaultChunkBytes)
		benchStore(b, a, v)
	})
	b.Run("struct", func(b *testing.B) {
		v := make([]row, 8)
		for i := range v {
			v[i] = row{id: int64(i), score: float64(i), name: "name"}
		}
		a := New[row](defaultChunkBytes)
		benchStore(b, a, v)
	})
}

// benchStore appends v repeatedly, rewinding every batch values so the arena carries its
// share of a Reset rather than growing without bound.
func benchStore[T any](b *testing.B, a *Arena[T], v []T) {
	b.Helper()
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		if n == batch {
			a.Reset()
			n = 0
		}
		_ = a.AppendRef(v)
		n++
	}
}
