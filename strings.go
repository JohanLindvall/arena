package arena

import (
	"strings"
	"unsafe"
)

// StringArena is an Arena[byte] with the string entry points added. It exists because a
// method cannot narrow its receiver's type parameter, so entry points that are only
// meaningful when the element type is byte cannot live on Arena itself.
//
// Everything Arena[byte] does is promoted, so a StringArena stores raw bytes and hands out
// Ref[byte] descriptors too, and the embedded field is addressable as a.Arena for code that
// wants the plain arena. The zero value is usable, with 64 KiB chunks.
//
// There are two ways to hold what it stores, matching the two on Arena. Intern hands back a
// string view, which is what a caller with a bounded number of live values wants; StrRef
// hands back a pointer-free descriptor that Str resolves, which is what a caller retaining
// a great many of them wants — a []Ref[byte] is 12 bytes per element and noscan, where a
// []string is 16 with a pointer in every one.
type StringArena struct {
	Arena[byte]
}

// NewStringArena returns a string arena with a chunk size of its own, as [New] does. Bytes
// and elements are the same thing here, so chunkBytes is exactly the chunk capacity.
func NewStringArena(chunkBytes int) *StringArena {
	return &StringArena{Arena: Arena[byte]{chunk: chunkElems[byte](chunkBytes)}}
}

// b2s returns a string view over b without copying. The caller must guarantee b
// is not mutated for the lifetime of the returned string.
func b2s(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// s2b returns a byte view over s without copying, for handing s to the storing side. The
// arena only ever reads from it, and a string is immutable, so nothing can write through.
func s2b(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

// Intern copies s into the arena and returns a stable string view over the copy.
// Strings larger than a chunk are not arena-backed: they get a standalone copy so
// the arena's reusable chunks stay a uniform size and a single huge string cannot
// pin an oversized chunk for the arena's lifetime.
func (a *StringArena) Intern(s string) string {
	if len(s) == 0 {
		return ""
	}
	if len(s) > a.chunkLen() {
		return strings.Clone(s) // a standalone copy, already immutable: no b2s aliasing concern
	}
	return b2s(a.Append(s2b(s)))
}

// StrRef copies s into the arena and returns a pointer-free descriptor for the copy — the
// string counterpart of AppendRef, resolved by Str. Reach for it over Intern when the batch
// retains enough strings for the descriptors themselves to matter (see [Ref]).
//
// Unlike Intern, an oversized string goes into a chunk of its own rather than a standalone
// copy, because a Ref can only address chunk storage. That chunk is NOT uniform, so it is
// kept by Reset and only freed by Release.
//
// The empty string gives the zero Ref, which Str resolves back to "".
func (a *StringArena) StrRef(s string) Ref[byte] {
	if len(s) == 0 {
		return Ref[byte]{}
	}
	return a.AppendRef(s2b(s))
}

// Str resolves r to a string view over the arena's copy — the string counterpart of Value.
// The absent descriptor resolves to the empty string.
func (a *StringArena) Str(r Ref[byte]) string {
	return b2s(a.Value(r))
}
