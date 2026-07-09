// Command paserati-wasm loads a WebAssembly module, compiles it to paserati
// bytecode (see docs/wasm-interop-design.md), and invokes an exported function.
//
//	paserati-wasm module.wasm                 # list exported functions
//	paserati-wasm module.wasm fib 20          # call fib(20)
//	paserati-wasm -list module.wasm           # list exports and exit
//
// Only the numeric + control-flow + call subset is supported; anything outside
// it fails with a clear message.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/nooga/paserati/pkg/vm"
	"github.com/nooga/paserati/pkg/wasm"
)

func main() {
	list := flag.Bool("list", false, "list exported functions and exit")
	run := flag.Bool("run", false, "run the WASI _start entry point (exits with its code)")
	profile := flag.Bool("profile", false, "report each function's register/jump profile vs paserati's limits")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	path := args[0]

	data, err := os.ReadFile(path)
	if err != nil {
		fatalf("read %s: %v", path, err)
	}
	mod, err := wasm.Decode(data)
	if err != nil {
		fatalf("decode: %v", err)
	}

	// -profile: report the register/jump profile vs paserati's bytecode limits.
	if *profile {
		mp, err := wasm.Profile(mod)
		if err != nil {
			fatalf("profile: %v", err)
		}
		fmt.Print(mp.String())
		return
	}

	// -run: WASI command mode — set up the host, call _start, exit with its code.
	// Any args after the module path become the guest's argv (e.g. "-" for stdin).
	if *run {
		code, err := wasm.RunStart(mod, os.Stdout, os.Stderr, args[1:]...)
		if err != nil {
			fatalf("run: %v", err)
		}
		os.Exit(code)
	}

	exports, err := wasm.CompileModule(mod)
	if err != nil {
		fatalf("compile: %v", err)
	}

	// No function named, or -list: show what's callable.
	if *list || len(args) < 2 {
		printExports(mod)
		return
	}

	name := args[1]
	fn, ok := exports[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "no exported function %q\n\n", name)
		printExports(mod)
		os.Exit(1)
	}

	sig, _ := funcSig(mod, name)
	callArgs, err := parseArgs(args[2:], sig)
	if err != nil {
		fatalf("%v", err)
	}

	machine := vm.NewVM()
	res, err := machine.Call(fn, vm.Undefined, callArgs)
	if err != nil {
		fatalf("call %s: %v", name, err)
	}
	fmt.Println(formatResult(res))
}

// parseArgs converts CLI number strings to VM values, checking arity.
func parseArgs(raw []string, sig *wasm.FuncType) ([]vm.Value, error) {
	if sig != nil && len(raw) != len(sig.Params) {
		return nil, fmt.Errorf("expected %d argument(s), got %d", len(sig.Params), len(raw))
	}
	out := make([]vm.Value, len(raw))
	for i, s := range raw {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("argument %q is not a number", s)
		}
		out[i] = vm.Number(f)
	}
	return out, nil
}

// formatResult prints whole numbers without a trailing ".0".
func formatResult(v vm.Value) string {
	f := v.ToFloat()
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func printExports(mod *wasm.Module) {
	fmt.Println("exported functions:")
	any := false
	for _, e := range mod.Exports {
		if e.Kind != 0 {
			continue
		}
		any = true
		if sig, ok := funcSig(mod, e.Name); ok {
			fmt.Printf("  %s%s\n", e.Name, sigString(sig))
		} else {
			fmt.Printf("  %s\n", e.Name)
		}
	}
	if !any {
		fmt.Println("  (none)")
	}
}

func funcSig(mod *wasm.Module, name string) (*wasm.FuncType, bool) {
	ex, ok := mod.FuncExport(name)
	if !ok {
		return nil, false
	}
	// Export indices count imported functions first; mod.Funcs holds only the
	// defined ones (mirrors the shift in CompileModuleWasi's export loop).
	d := int(ex.Index) - mod.ImportedFuncCount
	if d < 0 || d >= len(mod.Funcs) {
		return nil, false // re-exported import: no defined signature to show
	}
	return mod.Funcs[d].Type, true
}

func sigString(sig *wasm.FuncType) string {
	s := "("
	for i, p := range sig.Params {
		if i > 0 {
			s += ", "
		}
		s += p.String()
	}
	s += ")"
	if len(sig.Results) > 0 {
		s += " -> "
		for i, r := range sig.Results {
			if i > 0 {
				s += ", "
			}
			s += r.String()
		}
	}
	return s
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: paserati-wasm [-list] module.wasm [export] [args...]\n")
	flag.PrintDefaults()
}

func fatalf(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "paserati-wasm: "+format+"\n", a...)
	os.Exit(1)
}
