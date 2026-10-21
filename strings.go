// SPDX-License-Identifier: MIT

package arena

import "unsafe"

// StringArena is an Arena[byte] with the string entry points added. It exists because a
// method cannot narrow its receiver's type parameter, so entry points that are only
// meaningful when the element type is byte cannot live on Arena itself.
//
// Everything Arena[byte] does is promoted, so a StringArena stores raw bytes and hands out
// Ref[byte] descriptors too, and the embedded field is addressable as a.Arena for code that
// wants the plain arena. The zero value is usable, with 64 KiB chunks. Like Arena,
// StringArena must not be copied after first use and is not safe for concurrent use.
//
// There are two ways to hold what it stores, matching the two on Arena. AppendString
// hands back a string view, for a caller with a bounded number of live values.
// AppendStringRef hands back a pointer-free descriptor that StringValue resolves, for a
// caller retaining many values: a []Ref[byte] is 12 bytes per element and noscan, where a
// []string is 16 with a pointer in every element.
//
// Holding millions of strings as AppendStringRef descriptors reduces GC CPU load by removing
// per-string pointer scanning on every cycle. Neither the descriptor array nor the byte
// chunk contents need scanning; the arena's chunk headers and any live string views still
// do. Retain descriptors and resolve with StringValue as needed: keeping AppendString
// results or all resolved views in a []string preserves the per-string scan work.
type StringArena struct {
	Arena[byte]
}

// NewStringArena returns a string arena with a chunk size of its own, as [New] does. Bytes
// and elements are the same thing here, so max(1, chunkBytes) is the chunk capacity.
func NewStringArena(chunkBytes int) *StringArena {
	return &StringArena{Arena: Make[byte](chunkBytes)}
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

// AppendString copies s into the arena and returns a stable string view over the copy.
// A string larger than a whole chunk gets a chunk of its own, which Reset drops rather than
// recycling — see [Arena.Append]. Each call copies its input; equal strings are not
// deduplicated. The empty string stores nothing and returns "".
//
// Retaining the results in a []string still requires per-string GC scanning; use
// [StringArena.AppendStringRef] to avoid that CPU cost when holding many strings.
func (a *StringArena) AppendString(s string) string {
	if len(s) == 0 {
		return ""
	}
	return b2s(a.Append(s2b(s)))
}

// AppendStringRef copies s into the arena and returns a pointer-free descriptor for the
// copy — the string counterpart of AppendRef, resolved by StringValue. Use it when the
// batch retains many strings and GC scanning contributes to CPU load (see [Ref]). Retain
// the descriptors in a []Ref[byte] and resolve strings only as needed to avoid per-string
// scanning.
//
// The empty string gives the zero Ref, which StringValue resolves back to "".
//
// Like AppendRef, it panics if the stored range or chunk index exceeds the limits of
// [Ref], including strings of 2 GiB or more. The check follows the copy, so a recovered
// panic leaves the value stored but undescribed. AppendString has no such limit.
func (a *StringArena) AppendStringRef(s string) Ref[byte] {
	if len(s) == 0 {
		return Ref[byte]{}
	}
	return a.AppendRef(s2b(s))
}

// StringValue resolves r to a string view over the arena's copy — the string counterpart
// of Value.
// The absent descriptor resolves to the empty string. A nonempty r must belong to this
// arena's current batch; see [Ref]. The view stays valid until Reset or Release.
func (a *StringArena) StringValue(r Ref[byte]) string {
	return b2s(a.Value(r))
}
