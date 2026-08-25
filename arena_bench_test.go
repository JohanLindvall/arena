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
			a := New(defaultChunkSize)
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
	for _, size := range []int{16, 256, 4096, defaultChunkSize + 1} {
		value := make([]byte, size)

		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			a := New(defaultChunkSize)
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
		a := New(defaultChunkSize)
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
		a := New(defaultChunkSize)
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
		a := New(defaultChunkSize)
		refs := make([]Ref, n)
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
		a := New(defaultChunkSize)
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
			a := New(defaultChunkSize)
			refs := make([]Ref, 0, len(values))
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
