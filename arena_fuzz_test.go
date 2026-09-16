package arena_test

import (
	"bytes"
	"testing"

	"github.com/JohanLindvall/arena"
)

// Mix placement methods and batch boundaries while checking every live value.
// Small chunks exercise uniform and oversized storage without large allocations.
func FuzzArena(f *testing.F) {
	f.Add(uint8(8), []byte{24, 49, 74, 99, 3, 250, 201, 152, 4, 40})
	f.Add(uint8(1), []byte{0, 1, 2, 248, 249, 250, 3, 8, 9, 10})
	f.Fuzz(func(t *testing.T, budget uint8, operations []byte) {
		a := arena.New[byte](int(budget) + 1)
		type stored struct {
			want  []byte
			view  []byte
			ref   arena.Ref[byte]
			byRef bool
		}
		var values []stored
		size := 0
		for step, op := range operations[:min(len(operations), 512)] {
			switch op % 5 {
			case 3:
				values = nil // all views and references expire here
				size = 0
				a.Reset()
			case 4:
				values = nil
				size = 0
				a.Release()
				if a.Retained() != 0 {
					t.Fatal("Release retained chunk storage")
				}
			default:
				input := bytes.Repeat([]byte{byte(step % 256)}, int(op)/5)
				v := stored{want: bytes.Clone(input)}
				switch op % 5 {
				case 0:
					v.view = a.Append(input)
				case 1:
					v.ref, v.byRef = a.AppendRef(input), true
					if v.ref.Empty() != (len(input) == 0) {
						t.Fatal("Ref.Empty disagrees with input length")
					}
				case 2:
					v.view = a.Reserve(len(input))
					if len(v.view) != 0 || cap(v.view) != len(input) {
						t.Fatal("Reserve returned an incorrect length or capacity")
					}
					v.view = append(v.view, input...)
				}
				size += len(input)
				values = append(values, v)
				clear(input) // the arena must own an independent copy
			}
			if a.Size() != size || a.Retained() < size {
				t.Fatalf("step %d: size=%d retained=%d, want size=%d",
					step, a.Size(), a.Retained(), size)
			}
			for i, v := range values {
				got := v.view
				if v.byRef {
					got = a.Value(v.ref)
				}
				if !bytes.Equal(got, v.want) {
					t.Fatalf("step %d: value %d changed: got %v, want %v", step, i, got, v.want)
				}
			}
		}
	})
}
