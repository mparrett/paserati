package wasm

import (
	"fmt"
	"math"
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

	i64gtS, i64ltS vm.Value // i64 signed compares → boolean
	i64xor         vm.Value // i64 bitwise xor → BigInt

	extendU, extendS vm.Value // i32 → i64 (zero/sign extend)
	reinterpF32I32   vm.Value // f32 bits → i32
	reinterpF64I64   vm.Value // f64 bits → i64
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
	i64cmp := func(name string, f func(a, b int64) bool) vm.Value {
		return vm.NewNativeFunction(2, false, name, func(args []vm.Value) (vm.Value, error) {
			return vm.BooleanValue(f(asI64(args[0]), asI64(args[1]))), nil
		})
	}
	unary := func(name string, f func(v vm.Value) vm.Value) vm.Value {
		return vm.NewNativeFunction(1, false, name, func(args []vm.Value) (vm.Value, error) {
			return f(args[0]), nil
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

		i64gtS: i64cmp("i64.gt_s", func(a, b int64) bool { return a > b }),
		i64ltS: i64cmp("i64.lt_s", func(a, b int64) bool { return a < b }),
		i64xor: vm.NewNativeFunction(2, false, "i64.xor", func(args []vm.Value) (vm.Value, error) {
			return i64Value(asI64(args[0]) ^ asI64(args[1])), nil
		}),

		// i32 values are carried as signed floats; extend reinterprets as
		// unsigned (zero-extend) or keeps the sign (sign-extend) into an exact i64.
		extendU: unary("i64.extend_i32_u", func(v vm.Value) vm.Value { return i64Value(int64(asU32(v))) }),
		extendS: unary("i64.extend_i32_s", func(v vm.Value) vm.Value { return i64Value(int64(int32(v.ToFloat()))) }),
		reinterpF32I32: unary("i32.reinterpret_f32", func(v vm.Value) vm.Value {
			return vm.Number(float64(int32(math.Float32bits(float32(v.ToFloat())))))
		}),
		reinterpF64I64: unary("i64.reinterpret_f64", func(v vm.Value) vm.Value {
			return i64Value(int64(math.Float64bits(v.ToFloat())))
		}),
	}
}

// unaryHelper returns the helper for a one-operand conversion op, if op is one.
func (h *rtHelpers) unaryHelper(op Opcode) (vm.Value, bool) {
	switch op {
	case OpI64ExtendI32U:
		return h.extendU, true
	case OpI64ExtendI32S:
		return h.extendS, true
	case OpI32ReinterpretF32:
		return h.reinterpF32I32, true
	case OpI64ReinterpretF64:
		return h.reinterpF64I64, true
	}
	return vm.Undefined, false
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
	case OpI64GtS:
		return h.i64gtS, true
	case OpI64LtS:
		return h.i64ltS, true
	case OpI64Xor:
		return h.i64xor, true
	}
	return vm.Undefined, false
}
