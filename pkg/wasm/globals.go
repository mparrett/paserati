package wasm

import (
	"fmt"

	"github.com/nooga/paserati/pkg/vm"
)

// globals holds a module's global variables in a slice shared across every
// function (closed over by the get/set helpers), so a value written by one
// function is visible to another — registers can't do this, they're frame-local.
// Each global.get/set lowers to an OpCall into a helper with the global index
// folded in as a const argument, mirroring the memory helpers.
//
// Values are stored per type: Number (signed i32) for i32, the exact-BigInt
// carrier for i64, Number for f32/f64 — matching how each type is carried by
// the rest of the transpiled code (see globalInitValue).
type globals struct {
	values []vm.Value
	get    vm.Value
	set    vm.Value
}

// globalInitValue materialises a global's initialiser in the same carry
// representation the transpiled code uses for that type: exact BigInt for i64
// (Number lost precision past 2^53), the float bits for f32/f64 (previously
// decoded as zero), signed i32 otherwise.
func globalInitValue(g Global) vm.Value {
	switch g.Type {
	case I64:
		return i64Value(g.Init)
	case F32, F64:
		return vm.Number(g.FInit)
	default:
		return vm.Number(float64(int32(g.Init)))
	}
}

// newGlobals builds the storage (sized for imported + defined globals) and the
// helpers. Imported globals have no value here; accessing one faults.
func newGlobals(m *Module) *globals {
	imported := 0
	for _, im := range m.Imports {
		if im.Kind == ImportGlobal {
			imported++
		}
	}
	vals := make([]vm.Value, imported+len(m.Globals))
	for i := 0; i < imported; i++ {
		vals[i] = vm.Undefined // imported global values are unavailable
	}
	for i, g := range m.Globals {
		vals[imported+i] = globalInitValue(g)
	}

	idxOf := func(args []vm.Value) (int, error) {
		i := int(int32(args[len(args)-1].ToFloat()))
		if i < 0 || i >= len(vals) {
			return 0, fmt.Errorf("global %d out of range [0,%d)", i, len(vals))
		}
		return i, nil
	}
	g := &globals{values: vals}
	g.get = vm.NewNativeFunction(1, false, "global.get", func(args []vm.Value) (vm.Value, error) {
		i, err := idxOf(args)
		if err != nil {
			return vm.Undefined, err
		}
		return g.values[i], nil
	})
	g.set = vm.NewNativeFunction(2, false, "global.set", func(args []vm.Value) (vm.Value, error) {
		i, err := idxOf(args) // index is the last arg
		if err != nil {
			return vm.Undefined, err
		}
		g.values[i] = args[0] // value is the first arg
		return vm.Undefined, nil
	})
	return g
}

// emitGlobalGet lowers `global.get idx` → helper call `get(idx)` (result pushed).
func (g *funcGen) emitGlobalGet(idx uint32) error {
	if g.glob == nil {
		return fmt.Errorf("global.get without declared globals")
	}
	t := g.base + g.depth
	if t+2 > g.maxReg {
		g.maxReg = t + 2
	}
	g.loadConst(byte(t), g.c.AddConstant(g.glob.get))
	g.loadConst(byte(t+1), g.c.AddConstant(vm.Number(float64(idx))))
	dest := g.push()
	g.emitCallOp(dest, byte(t), 1)
	return nil
}

// emitGlobalSet lowers `global.set idx` → helper call `set(value, idx)` (void).
func (g *funcGen) emitGlobalSet(idx uint32) error {
	if g.glob == nil {
		return fmt.Errorf("global.set without declared globals")
	}
	val := byte(g.base + g.depth - 1)
	t := g.base + g.depth
	if t+3 > g.maxReg {
		g.maxReg = t + 3
	}
	g.loadConst(byte(t), g.c.AddConstant(g.glob.set))
	g.emit(vm.OpMove, byte(t+1), val)                                // arg0 = value
	g.loadConst(byte(t+2), g.c.AddConstant(vm.Number(float64(idx)))) // arg1 = index
	g.depth--                                                        // pop value
	dest := byte(g.base + g.depth)
	g.emitCallOp(dest, byte(t), 2)
	return nil
}
