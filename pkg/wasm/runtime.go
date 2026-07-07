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

	// unaryOps collects the one-operand conversions/math (extend, reinterpret,
	// wrap, sign-extend, saturating truncation, f64 unary math) keyed by opcode.
	unaryOps map[Opcode]vm.Value

	// binaryOps collects the two-operand i64 arithmetic/bitwise/compare helpers
	// (i64 is BigInt, so none map onto paserati's number opcodes) keyed by opcode.
	binaryOps map[Opcode]vm.Value
}

// satI32S/satI32U/satI64S/satI64U implement saturating float→int truncation:
// truncate toward zero, clamp to the target range, NaN → 0. f32 and f64 sources
// share these because we carry f32 as float64.
func satI32S(f float64) int32 {
	if math.IsNaN(f) {
		return 0
	}
	t := math.Trunc(f)
	if t >= math.MaxInt32 {
		return math.MaxInt32
	}
	if t <= math.MinInt32 {
		return math.MinInt32
	}
	return int32(t)
}

func satI32U(f float64) uint32 {
	if math.IsNaN(f) || f <= 0 {
		return 0
	}
	t := math.Trunc(f)
	if t >= math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(t)
}

func satI64S(f float64) int64 {
	if math.IsNaN(f) {
		return 0
	}
	t := math.Trunc(f)
	if t >= 9223372036854775808.0 { // 2^63; MaxInt64 isn't exactly representable
		return math.MaxInt64
	}
	if t < -9223372036854775808.0 {
		return math.MinInt64
	}
	return int64(t)
}

func satI64U(f float64) uint64 {
	if math.IsNaN(f) || f <= 0 {
		return 0
	}
	t := math.Trunc(f)
	if t >= 18446744073709551616.0 { // 2^64
		return math.MaxUint64
	}
	return uint64(t)
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
	i64bin := func(name string, f func(a, b int64) int64) vm.Value {
		return vm.NewNativeFunction(2, false, name, func(args []vm.Value) (vm.Value, error) {
			return i64Value(f(asI64(args[0]), asI64(args[1]))), nil
		})
	}
	// i64 div/rem: trap on divide-by-zero and on the MinInt64/-1 signed overflow.
	i64div := func(name string, signed bool, f func(a, b int64) int64) vm.Value {
		return vm.NewNativeFunction(2, false, name, func(args []vm.Value) (vm.Value, error) {
			a, b := asI64(args[0]), asI64(args[1])
			if b == 0 {
				return vm.Undefined, fmt.Errorf("%s by zero", name)
			}
			if signed && a == math.MinInt64 && b == -1 {
				return vm.Undefined, fmt.Errorf("%s overflow", name)
			}
			return i64Value(f(a, b)), nil
		})
	}
	i64ucmp := func(name string, f func(a, b uint64) bool) vm.Value {
		return vm.NewNativeFunction(2, false, name, func(args []vm.Value) (vm.Value, error) {
			return vm.BooleanValue(f(uint64(asI64(args[0])), uint64(asI64(args[1])))), nil
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

		unaryOps: map[Opcode]vm.Value{
			// i32 values are carried as signed floats; extend reinterprets as
			// unsigned (zero-extend) or keeps the sign into an exact i64. wrap takes
			// an i64's low 32 bits back to i32.
			OpI64ExtendI32U: unary("i64.extend_i32_u", func(v vm.Value) vm.Value { return i64Value(int64(asU32(v))) }),
			OpI64ExtendI32S: unary("i64.extend_i32_s", func(v vm.Value) vm.Value { return i64Value(int64(int32(v.ToFloat()))) }),
			OpI32WrapI64:    unary("i32.wrap_i64", func(v vm.Value) vm.Value { return vm.Number(float64(int32(asI64(v)))) }),

			// Bit reinterpretations (no value change, just type).
			OpI32ReinterpretF32: unary("i32.reinterpret_f32", func(v vm.Value) vm.Value {
				return vm.Number(float64(int32(math.Float32bits(float32(v.ToFloat())))))
			}),
			OpI64ReinterpretF64: unary("i64.reinterpret_f64", func(v vm.Value) vm.Value {
				return i64Value(int64(math.Float64bits(v.ToFloat())))
			}),
			OpF32ReinterpretI32: unary("f32.reinterpret_i32", func(v vm.Value) vm.Value {
				return vm.Number(float64(math.Float32frombits(asU32(v))))
			}),
			OpF64ReinterpretI64: unary("f64.reinterpret_i64", func(v vm.Value) vm.Value {
				return vm.Number(math.Float64frombits(uint64(asI64(v))))
			}),

			// Numeric conversions. i32 is already carried as a float, so
			// convert_i32_s is identity; _u must reinterpret the sign bit. promote is
			// identity (f32 carried as f64); demote rounds to float32 precision.
			OpF64ConvertI32S: unary("f64.convert_i32_s", func(v vm.Value) vm.Value { return vm.Number(v.ToFloat()) }),
			OpF64ConvertI32U: unary("f64.convert_i32_u", func(v vm.Value) vm.Value { return vm.Number(float64(asU32(v))) }),
			OpF64ConvertI64S: unary("f64.convert_i64_s", func(v vm.Value) vm.Value { return vm.Number(float64(asI64(v))) }),
			OpF64ConvertI64U: unary("f64.convert_i64_u", func(v vm.Value) vm.Value { return vm.Number(float64(uint64(asI64(v)))) }),
			OpF64PromoteF32:  unary("f64.promote_f32", func(v vm.Value) vm.Value { return vm.Number(v.ToFloat()) }),
			OpF32DemoteF64:   unary("f32.demote_f64", func(v vm.Value) vm.Value { return vm.Number(float64(float32(v.ToFloat()))) }),

			// Saturating float→int truncation.
			OpI32TruncSatS: unary("i32.trunc_sat_s", func(v vm.Value) vm.Value { return vm.Number(float64(satI32S(v.ToFloat()))) }),
			OpI32TruncSatU: unary("i32.trunc_sat_u", func(v vm.Value) vm.Value { return vm.Number(float64(int32(satI32U(v.ToFloat())))) }),
			OpI64TruncSatS: unary("i64.trunc_sat_s", func(v vm.Value) vm.Value { return i64Value(satI64S(v.ToFloat())) }),
			OpI64TruncSatU: unary("i64.trunc_sat_u", func(v vm.Value) vm.Value { return i64Value(int64(satI64U(v.ToFloat()))) }),

			// f64 unary math.
			OpF64Neg:  unary("f64.neg", func(v vm.Value) vm.Value { return vm.Number(-v.ToFloat()) }),
			OpF64Abs:  unary("f64.abs", func(v vm.Value) vm.Value { return vm.Number(math.Abs(v.ToFloat())) }),
			OpF64Sqrt: unary("f64.sqrt", func(v vm.Value) vm.Value { return vm.Number(math.Sqrt(v.ToFloat())) }),

			// Sign-extension: keep the low N bits, extend the sign.
			OpI32Extend8S:  unary("i32.extend8_s", func(v vm.Value) vm.Value { return vm.Number(float64(int32(int8(asU32(v))))) }),
			OpI32Extend16S: unary("i32.extend16_s", func(v vm.Value) vm.Value { return vm.Number(float64(int32(int16(asU32(v))))) }),
			OpI64Extend8S:  unary("i64.extend8_s", func(v vm.Value) vm.Value { return i64Value(int64(int8(asI64(v)))) }),
			OpI64Extend16S: unary("i64.extend16_s", func(v vm.Value) vm.Value { return i64Value(int64(int16(asI64(v)))) }),
			OpI64Extend32S: unary("i64.extend32_s", func(v vm.Value) vm.Value { return i64Value(int64(int32(asI64(v)))) }),

			// i32/i64 bit-count and i64 test-for-zero.
			OpI32Clz:    unary("i32.clz", func(v vm.Value) vm.Value { return vm.Number(float64(bits.LeadingZeros32(asU32(v)))) }),
			OpI32Ctz:    unary("i32.ctz", func(v vm.Value) vm.Value { return vm.Number(float64(bits.TrailingZeros32(asU32(v)))) }),
			OpI32Popcnt: unary("i32.popcnt", func(v vm.Value) vm.Value { return vm.Number(float64(bits.OnesCount32(asU32(v)))) }),
			OpI64Clz:    unary("i64.clz", func(v vm.Value) vm.Value { return i64Value(int64(bits.LeadingZeros64(uint64(asI64(v))))) }),
			OpI64Ctz:    unary("i64.ctz", func(v vm.Value) vm.Value { return i64Value(int64(bits.TrailingZeros64(uint64(asI64(v))))) }),
			OpI64Popcnt: unary("i64.popcnt", func(v vm.Value) vm.Value { return i64Value(int64(bits.OnesCount64(uint64(asI64(v))))) }),
			OpI64Eqz:    unary("i64.eqz", func(v vm.Value) vm.Value { return vm.BooleanValue(asI64(v) == 0) }),
		},

		binaryOps: map[Opcode]vm.Value{
			// i32 signed div/rem: truncated toward zero (paserati's OpDivide is
			// float division, so these can't go through binopOp). Trap on zero and
			// on the MinInt32/-1 div overflow; rem of that case is 0.
			OpI32DivS: vm.NewNativeFunction(2, false, "i32.div_s", func(args []vm.Value) (vm.Value, error) {
				a, b := int32(args[0].ToFloat()), int32(args[1].ToFloat())
				if b == 0 {
					return vm.Undefined, fmt.Errorf("i32.div_s by zero")
				}
				if a == math.MinInt32 && b == -1 {
					return vm.Undefined, fmt.Errorf("i32.div_s overflow")
				}
				return vm.Number(float64(a / b)), nil
			}),
			OpI32RemS: vm.NewNativeFunction(2, false, "i32.rem_s", func(args []vm.Value) (vm.Value, error) {
				a, b := int32(args[0].ToFloat()), int32(args[1].ToFloat())
				if b == 0 {
					return vm.Undefined, fmt.Errorf("i32.rem_s by zero")
				}
				if a == math.MinInt32 && b == -1 {
					return vm.Number(0), nil
				}
				return vm.Number(float64(a % b)), nil
			}),

			// Arithmetic / bitwise → i64 (Go int64 wraps mod 2^64, matching wasm).
			OpI64Sub:  i64bin("i64.sub", func(a, b int64) int64 { return a - b }),
			OpI64Mul:  i64bin("i64.mul", func(a, b int64) int64 { return a * b }),
			OpI64And:  i64bin("i64.and", func(a, b int64) int64 { return a & b }),
			OpI64Or:   i64bin("i64.or", func(a, b int64) int64 { return a | b }),
			OpI64Shl:  i64bin("i64.shl", func(a, b int64) int64 { return a << uint(b&63) }),
			OpI64ShrS: i64bin("i64.shr_s", func(a, b int64) int64 { return a >> uint(b&63) }),
			OpI64ShrU: i64bin("i64.shr_u", func(a, b int64) int64 { return int64(uint64(a) >> uint(b&63)) }),
			OpI64Rotl: i64bin("i64.rotl", func(a, b int64) int64 { return int64(bits.RotateLeft64(uint64(a), int(b&63))) }),
			OpI64Rotr: i64bin("i64.rotr", func(a, b int64) int64 { return int64(bits.RotateLeft64(uint64(a), -int(b&63))) }),
			OpI64DivS: i64div("i64.div_s", true, func(a, b int64) int64 { return a / b }),
			OpI64DivU: i64div("i64.div_u", false, func(a, b int64) int64 { return int64(uint64(a) / uint64(b)) }),
			OpI64RemS: i64div("i64.rem_s", false, func(a, b int64) int64 { // rem never overflows; MinInt64%-1 == 0
				if a == math.MinInt64 && b == -1 {
					return 0
				}
				return a % b
			}),
			OpI64RemU: i64div("i64.rem_u", false, func(a, b int64) int64 { return int64(uint64(a) % uint64(b)) }),

			// Comparisons → boolean.
			OpI64Eq:  i64cmp("i64.eq", func(a, b int64) bool { return a == b }),
			OpI64Ne:  i64cmp("i64.ne", func(a, b int64) bool { return a != b }),
			OpI64LeS: i64cmp("i64.le_s", func(a, b int64) bool { return a <= b }),
			OpI64GeS: i64cmp("i64.ge_s", func(a, b int64) bool { return a >= b }),
			OpI64LtU: i64ucmp("i64.lt_u", func(a, b uint64) bool { return a < b }),
			OpI64GtU: i64ucmp("i64.gt_u", func(a, b uint64) bool { return a > b }),
			OpI64LeU: i64ucmp("i64.le_u", func(a, b uint64) bool { return a <= b }),
			OpI64GeU: i64ucmp("i64.ge_u", func(a, b uint64) bool { return a >= b }),
		},
	}
}

// binaryHelper returns the helper for a two-operand i64 op, if op is one.
func (h *rtHelpers) binaryHelper(op Opcode) (vm.Value, bool) {
	v, ok := h.binaryOps[op]
	return v, ok
}

// unaryHelper returns the helper for a one-operand conversion/math op, if any.
func (h *rtHelpers) unaryHelper(op Opcode) (vm.Value, bool) {
	v, ok := h.unaryOps[op]
	return v, ok
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
