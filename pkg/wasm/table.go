package wasm

import (
	"fmt"

	"github.com/nooga/paserati/pkg/vm"
)

// funcTable backs a module's function table (table 0) for call_indirect. Slots
// are filled from the active element segments; each holds the callable Value and
// its signature so the indirect call can trap on an out-of-range index, an empty
// slot, or a type mismatch — the three call_indirect traps.
//
// paserati has no computed dispatch, so the lookup is a native helper: given a
// runtime index and the call site's static type index, it returns the callee
// Value (or errors, which the VM turns into a trap). The call site then does an
// ordinary OpCall on the returned Value.
type funcTable struct {
	get vm.Value // native (idx, typeIdx) -> callee Value
}

type tableSlot struct {
	val vm.Value
	sig *FuncType
	set bool
}

// newFuncTable resolves every element-segment entry to a callable Value. Returns
// nil when the module declares no table.
func newFuncTable(m *Module, vals []vm.Value, binds []importBinding) *funcTable {
	if len(m.Tables) == 0 {
		return nil
	}
	slots := make([]tableSlot, int(m.Tables[0].Min))
	for _, e := range m.Elems {
		for j, fi := range e.FuncIndices {
			idx := e.Offset + j
			if idx >= len(slots) { // segment past declared min: grow to fit
				grown := make([]tableSlot, idx+1)
				copy(grown, slots)
				slots = grown
			}
			val, sig := resolveFuncRef(m, vals, binds, fi)
			slots[idx] = tableSlot{val: val, sig: sig, set: true}
		}
	}

	get := vm.NewNativeFunction(2, false, "table.get", func(args []vm.Value) (vm.Value, error) {
		i := int(int32(args[0].ToFloat()))
		typeIdx := uint32(int32(args[1].ToFloat()))
		if i < 0 || i >= len(slots) || !slots[i].set {
			return vm.Undefined, fmt.Errorf("call_indirect: undefined element %d", i)
		}
		if int(typeIdx) >= len(m.Types) || !sigEqual(slots[i].sig, &m.Types[typeIdx]) {
			return vm.Undefined, fmt.Errorf("call_indirect: signature mismatch at element %d", i)
		}
		return slots[i].val, nil
	})
	return &funcTable{get: get}
}

// resolveFuncRef maps a wasm function index (imports first) to its callable Value
// and signature.
func resolveFuncRef(m *Module, vals []vm.Value, binds []importBinding, funcIdx uint32) (vm.Value, *FuncType) {
	imported := m.ImportedFuncCount
	if int(funcIdx) < imported {
		b := binds[funcIdx]
		return b.val, b.sig
	}
	d := int(funcIdx) - imported
	return vals[d], m.Funcs[d].Type
}

// sigEqual reports structural equality of two function signatures — call_indirect
// checks type identity, and TinyGo/LLVM may assign distinct type indices to
// identical shapes.
func sigEqual(a, b *FuncType) bool {
	if a == nil || b == nil || len(a.Params) != len(b.Params) || len(a.Results) != len(b.Results) {
		return false
	}
	for i := range a.Params {
		if a.Params[i] != b.Params[i] {
			return false
		}
	}
	for i := range a.Results {
		if a.Results[i] != b.Results[i] {
			return false
		}
	}
	return true
}
