package wasm

import (
	"fmt"
	"io"

	"github.com/nooga/paserati/pkg/vm"
)

// RunStart compiles a WASI command module and invokes its `_start` export,
// returning the process exit code. proc_exit(code) ends the run with that code
// (the sentinel error it throws to unwind the VM is expected, not a failure); a
// normal return from _start is exit 0. Writers default to the process stdio when
// nil.
func RunStart(m *Module, stdout, stderr io.Writer) (int, error) {
	exports, host, err := CompileModuleWasi(m, stdout, stderr)
	if err != nil {
		return 0, err
	}
	start, ok := exports["_start"]
	if !ok {
		return 0, fmt.Errorf("module has no _start export (not a WASI command)")
	}

	machine := vm.NewVM()
	_, callErr := machine.Call(start, vm.Undefined, nil)

	// proc_exit is the normal way a WASI command ends; its unwinding error is
	// expected and superseded by the recorded exit code.
	if host != nil && host.exited {
		return int(host.exitCode), nil
	}
	if callErr != nil {
		return 0, callErr
	}
	return 0, nil
}
