package wasm

import "testing"

// TestI32WrapSemantics locks the high-bit i32 semantics: add/sub/mul wrap mod
// 2^32, and unsigned ops (div_u/rem_u/shr_u/rotl) return the signed-i32 carry
// representation, including when the result feeds a signed compare or eq.
// Expected values cross-checked against wasmtime on testdata/i32wrap.wat.
func TestI32WrapSemantics(t *testing.T) {
	const path = "testdata/i32wrap.wasm"
	cases := []struct {
		export string
		args   []float64
		want   float64
	}{
		{"add", []float64{2147483647, 1}, -2147483648},
		{"add", []float64{-2147483648, -1}, 2147483647},
		{"sub", []float64{-2147483648, 1}, 2147483647},
		{"mul", []float64{3, -5}, -15},
		{"mul", []float64{65536, 65536}, 0},           // 2^32 wraps to 0
		{"mul", []float64{2147483647, 2147483647}, 1}, // (2^31-1)^2 ≡ 1 mod 2^32
		{"div_u", []float64{-1, 1}, -1},               // 0xFFFFFFFF re-signed
		{"div_u", []float64{-2, 2}, 2147483647},       // 0xFFFFFFFE/2
		{"rem_u", []float64{-1, 10}, 5},               // 4294967295 % 10
		{"shr_u", []float64{-1, 0}, -1},               // bit pattern unchanged
		{"shr_u", []float64{-1, 1}, 2147483647},
		{"rotl", []float64{-1, 1}, -1},                  // all-ones rotates to itself
		{"rotl", []float64{1073741824, 1}, -2147483648}, // 0x40000000 → 0x80000000
		{"divu_then_lts", []float64{-1, 1, 0}, 1},       // div_u result must compare signed
		{"divu_then_eq", []float64{-1, 1, -1}, 1},       // ...and be eq to its signed rep
	}
	for _, c := range cases {
		m, fn := compileExport(t, path, c.export)
		if got := callI(t, m, fn, c.args...); got != c.want {
			t.Errorf("%s(%v) = %v, want %v", c.export, c.args, got, c.want)
		}
	}
}
