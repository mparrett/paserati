package vm

import (
	"runtime"
	"testing"
)

// isObjectMix models the hot-path distribution: IsObject is called predominantly
// on non-object values (numbers in arithmetic/ToInteger guards), which is the
// OR-chain's worst case (all comparisons fail before returning false).
func isObjectMix() []Value {
	vs := make([]Value, 0, 32)
	for i := 0; i < 24; i++ {
		vs = append(vs, IntegerValue(int32(i))) // 24 numbers (typ far below the object range)
	}
	vs = append(vs,
		NewString("x"), BooleanValue(true), Undefined, Null, // more non-objects
		Value{typ: TypeObject}, Value{typ: TypeArray},
		Value{typ: TypeProxy}, Value{typ: TypeMap}, // a few objects
	)
	return vs
}

// The accumulator must be escaped, not discarded with `_ = sink`. The OR-chain
// form of IsObject costs 90 nodes against an inline budget of 80, so it was a
// real call the compiler could not see through; the range-check form inlines.
// That made the two commits unequally vulnerable to dead-code elimination —
// with a discarded accumulator the inlined form's loop body was removed
// outright and the benchmark measured an empty loop, exaggerating the A/B gain.
// runtime.KeepAlive is the same guard BenchmarkRatchetAnchor already uses.
func BenchmarkIsObject(b *testing.B) {
	vs := isObjectMix()
	b.ResetTimer()
	var sink int
	// Inner range loop so IsObject dominates ns/op instead of index arithmetic.
	// ns/op therefore covers len(vs) IsObject calls; consistent across A/B runs.
	for i := 0; i < b.N; i++ {
		for j := range vs {
			if vs[j].IsObject() {
				sink++
			}
		}
	}
	runtime.KeepAlive(sink)
}

// TestIsObjectExhaustive locks in that IsObject is equivalent to the contiguous
// [TypeObject, TypeProxy] range for every defined ValueType — the invariant the
// range-check implementation depends on. If a new type is inserted mid-enum this
// fails, flagging that the range bounds (and this list) need review.
func TestIsObjectExhaustive(t *testing.T) {
	// Every type that must be an object (matches the historical OR-chain).
	objectTypes := map[ValueType]bool{
		TypeObject: true, TypeDictObject: true, TypeArray: true, TypeArguments: true,
		TypeGenerator: true, TypeAsyncGenerator: true, TypePromise: true, TypeRegExp: true,
		TypeTypedArray: true, TypeDataView: true, TypeArrayBuffer: true, TypeSharedArrayBuffer: true,
		TypeProxy: true, TypeMap: true, TypeSet: true, TypeWeakMap: true, TypeWeakSet: true,
		TypeWeakRef: true,
	}
	for typ := TypeUndefined; typ <= TypeUninitialized; typ++ {
		got := (Value{typ: typ}).IsObject()
		want := objectTypes[typ]
		if got != want {
			t.Errorf("IsObject(typ=%d) = %v, want %v", typ, got, want)
		}
		// Range-check equivalence: object types must be exactly the contiguous span.
		if inRange := typ >= TypeObject && typ <= TypeProxy; inRange != want {
			t.Errorf("type %d: range [TypeObject,TypeProxy] membership %v != object-set %v "+
				"(object types are no longer contiguous — fix IsObject bounds)", typ, inRange, want)
		}
	}
}
