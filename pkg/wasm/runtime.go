package wasm

import (
	"fmt"
	"math/bits"

	"github.com/nooga/paserati/pkg/vm"
)

// rtHelpers are stateless native helpers shared across a module's functions for
// ops that don't map cleanly onto paserati's JS-semantics opcodes: exact i64.add
// and the unsigned i32 comparisons / division. i32 values are carried as signed
// floats, so unsigned ops must reinterpret them as uint32 first.
type rtHelpers struct {
	i64add             vm.Value
	gtU, geU, ltU, leU vm.Value // unsigned compares → boolean
	divU, remU         vm.Value // unsigned div/rem → uint32 as Number
	rotl, rotr         vm.Value // 32-bit rotates (no paserati opcode)
}

// asU32 reinterprets a carried i32 value (a signed float) as unsigned 32-bit.
func asU32(v vm.Value) uint32 { return uint32(int32(v.ToFloat())) }

func newRTHelpers() *rtHelpers {
	cmp := func(name string, f func(a, b uint32) bool) vm.Value {
		return vm.NewNativeFunction(2, false, name, func(args []vm.Value) (vm.Value, error) {
			return vm.BooleanValue(f(asU32(args[0]), asU32(args[1]))), nil
		})
	}
	div := func(name string, f func(a, b uint32) uint32) vm.Value {
		return vm.NewNativeFunction(2, false, name, func(args []vm.Value) (vm.Value, error) {
			a, b := asU32(args[0]), asU32(args[1])
			if b == 0 {
				return vm.Undefined, fmt.Errorf("%s by zero", name)
			}
			return vm.Number(float64(f(a, b))), nil
		})
	}
	rot := func(name string, f func(a uint32, n int) uint32) vm.Value {
		return vm.NewNativeFunction(2, false, name, func(args []vm.Value) (vm.Value, error) {
			return vm.Number(float64(f(asU32(args[0]), int(asU32(args[1])&31)))), nil
		})
	}
	return &rtHelpers{
		i64add: makeI64Add(),
		gtU:    cmp("i32.gt_u", func(a, b uint32) bool { return a > b }),
		geU:    cmp("i32.ge_u", func(a, b uint32) bool { return a >= b }),
		ltU:    cmp("i32.lt_u", func(a, b uint32) bool { return a < b }),
		leU:    cmp("i32.le_u", func(a, b uint32) bool { return a <= b }),
		divU:   div("i32.div_u", func(a, b uint32) uint32 { return a / b }),
		remU:   div("i32.rem_u", func(a, b uint32) uint32 { return a % b }),
		rotl:   rot("i32.rotl", func(a uint32, n int) uint32 { return bits.RotateLeft32(a, n) }),
		rotr:   rot("i32.rotr", func(a uint32, n int) uint32 { return bits.RotateLeft32(a, -n) }),
	}
}

// unsignedHelper returns the helper for an unsigned i32 op, if op is one.
func (h *rtHelpers) unsignedHelper(op Opcode) (vm.Value, bool) {
	switch op {
	case OpI32GtU:
		return h.gtU, true
	case OpI32GeU:
		return h.geU, true
	case OpI32LtU:
		return h.ltU, true
	case OpI32LeU:
		return h.leU, true
	case OpI32DivU:
		return h.divU, true
	case OpI32RemU:
		return h.remU, true
	case OpI32Rotl:
		return h.rotl, true
	case OpI32Rotr:
		return h.rotr, true
	}
	return vm.Undefined, false
}
