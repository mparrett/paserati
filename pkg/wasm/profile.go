package wasm

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nooga/paserati/pkg/vm"
)

// FuncProfile is the register/jump profile of one function: how it lowers and
// whether it fits paserati's bytecode-format limits (256 registers, ±32 KB jumps).
type FuncProfile struct {
	Index     int
	Name      string
	NumParams int
	NumLocals int
	Mode      string // "direct", "spilled", "stubbed", or "error"
	MaxReg    int
	MaxJump   int
	Limit     string // for stubbed: "registers" or "jumps"
	Err       string // for mode "error"
}

// ModuleProfile summarizes a whole module against the two limits.
type ModuleProfile struct {
	Funcs      []FuncProfile
	NumFuncs   int
	NumDirect  int
	NumSpilled int
	NumStubbed int
	NumErr     int
}

const regLimit = 256

// Profile compiles every function (mirroring CompileModuleWasi's setup and its
// direct→spill→stub fallback) but records stats instead of encoding, reporting
// whether the module fits the register and jump limits.
func Profile(m *Module) (*ModuleProfile, error) {
	var mem *memory
	if m.Memory != nil {
		var err error
		if mem, err = newMemory(m); err != nil {
			return nil, err
		}
	}
	var glob *globals
	if len(m.Globals) > 0 {
		glob = newGlobals(m)
	}
	rt := newRTHelpers()
	var binds []importBinding
	if m.ImportedFuncCount > 0 && mem != nil {
		binds, _ = buildImportBindings(m, newWASIHost(mem.getData, nil, nil))
	}
	vals := make([]vm.Value, len(m.Funcs))
	for i := range m.Funcs {
		fn := &m.Funcs[i]
		vals[i] = vm.NewFunction(fn.NumParams(), fn.NumParams(), 0, 0, false,
			funcName(m, i), vm.NewChunk(), false, false, false, false)
	}
	table := newFuncTable(m, vals, binds)

	mp := &ModuleProfile{NumFuncs: len(m.Funcs)}
	for i := range m.Funcs {
		fp := profileFunc(m, i, vals, mem, glob, rt, binds, table)
		mp.Funcs = append(mp.Funcs, fp)
		switch fp.Mode {
		case "direct":
			mp.NumDirect++
		case "spilled":
			mp.NumSpilled++
		case "stubbed":
			mp.NumStubbed++
		case "error":
			mp.NumErr++
		}
	}
	return mp, nil
}

func profileFunc(m *Module, i int, vals []vm.Value, mem *memory, glob *globals,
	rt *rtHelpers, binds []importBinding, table *funcTable) FuncProfile {
	fp := FuncProfile{
		Index: i, Name: funcName(m, i),
		NumParams: m.Funcs[i].NumParams(), NumLocals: m.Funcs[i].NumLocals(),
	}
	mk := func(spill bool) *funcGen {
		return &funcGen{c: vm.NewChunk(), fn: &m.Funcs[i], mod: m, vals: vals,
			mem: mem, glob: glob, rt: rt, imports: binds, table: table, spill: spill}
	}
	// emit reports (maxReg, maxJump, overflow, hardErr) for a lowering attempt.
	emit := func(spill bool) (int, int, bool, error) {
		g := mk(spill)
		if err := g.emitAll(); err != nil {
			if errors.Is(err, errRegOverflow) {
				return g.maxReg, 0, true, nil
			}
			return 0, 0, false, err // unsupported opcode etc.
		}
		mj := g.layoutMaxJump()
		// Jumps past int16 are promoted to 32-bit long jumps at encode time, so
		// jump distance no longer forces a stub — only the register file can.
		return g.maxReg, mj, g.maxReg > regLimit, nil
	}

	maxReg, maxJump, overflow, err := emit(false)
	if err != nil {
		fp.Mode, fp.Err = "error", err.Error()
		return fp
	}
	if !overflow {
		fp.Mode, fp.MaxReg, fp.MaxJump = "direct", maxReg, maxJump
		return fp
	}
	// Retry spilled (locals in an array), like the real compiler.
	maxReg, maxJump, overflow, err = emit(true)
	if err != nil {
		fp.Mode, fp.Err = "error", err.Error()
		return fp
	}
	fp.MaxReg, fp.MaxJump = maxReg, maxJump
	if !overflow {
		fp.Mode = "spilled"
		return fp
	}
	// Long jumps cover any distance, so the register file is the only remaining
	// limit that can force a stub.
	fp.Mode = "stubbed"
	fp.Limit = "registers"
	return fp
}

// String renders a human summary plus the details that matter for the fork
// decision: the biggest functions and anything that doesn't fit.
func (mp *ModuleProfile) String() string {
	var b strings.Builder
	verdict := "RUNS — every function fits (256 regs; jumps use 32-bit long form as needed)"
	if mp.NumStubbed > 0 || mp.NumErr > 0 {
		verdict = "DOES NOT RUN — see below"
	}
	fmt.Fprintf(&b, "verdict: %s\n", verdict)
	fmt.Fprintf(&b, "%d funcs: %d direct, %d spilled, %d stubbed, %d error\n",
		mp.NumFuncs, mp.NumDirect, mp.NumSpilled, mp.NumStubbed, mp.NumErr)

	// Anything stubbed or errored — the blockers.
	for _, f := range mp.Funcs {
		if f.Mode == "stubbed" {
			fmt.Fprintf(&b, "  STUB  %-40s locals=%d maxReg=%d maxJump=%d over=%s\n",
				short(f.Name), f.NumLocals, f.MaxReg, f.MaxJump, f.Limit)
		}
		if f.Mode == "error" {
			fmt.Fprintf(&b, "  ERROR %-40s %s\n", short(f.Name), f.Err)
		}
	}

	// Top 5 by locals and by jump distance, for headroom context.
	byLocals := append([]FuncProfile(nil), mp.Funcs...)
	sort.Slice(byLocals, func(a, c int) bool { return byLocals[a].NumLocals > byLocals[c].NumLocals })
	fmt.Fprintf(&b, "top locals:")
	for _, f := range byLocals[:min(5, len(byLocals))] {
		fmt.Fprintf(&b, " %d(%s)", f.NumLocals, f.Mode)
	}
	byJump := append([]FuncProfile(nil), mp.Funcs...)
	sort.Slice(byJump, func(a, c int) bool { return byJump[a].MaxJump > byJump[c].MaxJump })
	fmt.Fprintf(&b, "\ntop jumps:")
	for _, f := range byJump[:min(5, len(byJump))] {
		fmt.Fprintf(&b, " %d(%s)", f.MaxJump, f.Mode)
	}
	b.WriteString("\n")
	return b.String()
}

func short(name string) string {
	if len(name) > 40 {
		return name[:37] + "..."
	}
	return name
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
