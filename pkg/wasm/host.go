package wasm

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/nooga/paserati/pkg/vm"
)

// wasiHost implements the sliver of wasi_snapshot_preview1 a TinyGo
// `-target=wasi` hello-world needs: fd_write, proc_exit, random_get. The three
// imports lower to OpCall into native helpers that close over the module's
// linear-memory bytes, exactly like the load/store helpers in memory.go. Because
// memory.grow is unsupported the backing slice never moves, so closing over it is
// safe (see the memory type's note).
//
// WASI values are all i32, carried as signed float64 here (the codegen fidelity
// gap). Pointers and lengths stay well inside 2^31, so the reinterpretation is
// lossless in practice. errno results are returned as Number(0) for success —
// this subset never surfaces a real error code to the guest.
type wasiHost struct {
	// getData returns the live memory slice, following memory.grow reallocations
	// (a captured slice would go stale after a grow).
	getData func() []byte
	stdout  io.Writer
	stderr  io.Writer

	// proc_exit records its state here rather than relying on the unwinding
	// error surviving the VM's exception machinery: a native error is wrapped
	// into a thrown JS Error, so the runner reads exited/exitCode instead of
	// pattern-matching the propagated error string.
	exited   bool
	exitCode int32
}

// newWASIHost builds a host over a memory's live-data getter. nil writers default
// to the process stdio so the CLI runner needs no wiring; tests pass buffers.
func newWASIHost(getData func() []byte, stdout, stderr io.Writer) *wasiHost {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return &wasiHost{getData: getData, stdout: stdout, stderr: stderr}
}

// WASI errno values this subset can return.
const (
	wasiESuccess = 0
	wasiEFault   = 21 // __WASI_ERRNO_FAULT — an out-of-bounds pointer
)

// asU32Arg reinterprets a WASI i32 argument (carried as a signed float) as an
// unsigned byte offset/length.
func asU32Arg(v vm.Value) uint32 { return uint32(int32(v.ToFloat())) }

// inBounds reports whether [off, off+n) is inside data, guarding the reads and
// writes the guest asks for.
func inBounds(data []byte, off, n uint32) bool {
	end := uint64(off) + uint64(n)
	return end <= uint64(len(data))
}

// fdWrite implements fd_write(fd, iovs, iovs_len, nwritten) -> errno. It gathers
// the iovec buffers (8 bytes each: {buf: u32, len: u32}) from memory, writes them
// to the fd's stream (1=stdout, 2=stderr; others error), and stores the total
// byte count at *nwritten.
func (h *wasiHost) fdWrite(args []vm.Value) (vm.Value, error) {
	fd := int32(args[0].ToFloat())
	iovs := asU32Arg(args[1])
	iovsLen := asU32Arg(args[2])
	nwritten := asU32Arg(args[3])

	var w io.Writer
	switch fd {
	case 1:
		w = h.stdout
	case 2:
		w = h.stderr
	default:
		return vm.Number(wasiEFault), nil // only stdout/stderr in this subset
	}

	data := h.getData()
	var total uint32
	for i := uint32(0); i < iovsLen; i++ {
		rec := iovs + i*8
		if !inBounds(data, rec, 8) {
			return vm.Number(wasiEFault), nil
		}
		buf := binary.LittleEndian.Uint32(data[rec:])
		length := binary.LittleEndian.Uint32(data[rec+4:])
		if !inBounds(data, buf, length) {
			return vm.Number(wasiEFault), nil
		}
		if length == 0 {
			continue
		}
		if _, err := w.Write(data[buf : buf+length]); err != nil {
			return vm.Undefined, err
		}
		total += length
	}

	if !inBounds(data, nwritten, 4) {
		return vm.Number(wasiEFault), nil
	}
	binary.LittleEndian.PutUint32(data[nwritten:], total)
	return vm.Number(wasiESuccess), nil
}

// procExit implements proc_exit(code). It records the exit and returns a sentinel
// error to unwind the VM; the runner reads h.exited/h.exitCode.
func (h *wasiHost) procExit(args []vm.Value) (vm.Value, error) {
	h.exited = true
	h.exitCode = int32(args[0].ToFloat())
	return vm.Undefined, wasiExit{code: h.exitCode}
}

// randomGet implements random_get(buf, len) -> errno, filling memory[buf:buf+len]
// with cryptographically-random bytes.
func (h *wasiHost) randomGet(args []vm.Value) (vm.Value, error) {
	buf := asU32Arg(args[0])
	length := asU32Arg(args[1])
	data := h.getData()
	if !inBounds(data, buf, length) {
		return vm.Number(wasiEFault), nil
	}
	if _, err := rand.Read(data[buf : buf+length]); err != nil {
		return vm.Undefined, err
	}
	return vm.Number(wasiESuccess), nil
}

// wasiExit is the sentinel proc_exit throws to unwind _start.
type wasiExit struct{ code int32 }

func (e wasiExit) Error() string { return fmt.Sprintf("wasi: proc_exit(%d)", e.code) }

// resolve maps an import's (module, field) to its native helper Value. Unknown
// imports fail at compile time with a clear message.
func (h *wasiHost) resolve(module, field string) (vm.Value, error) {
	if module != "wasi_snapshot_preview1" {
		return vm.Undefined, fmt.Errorf("unsupported import module %q", module)
	}
	switch field {
	case "fd_write":
		return vm.NewNativeFunction(4, false, "wasi.fd_write", h.fdWrite), nil
	case "proc_exit":
		return vm.NewNativeFunction(1, false, "wasi.proc_exit", h.procExit), nil
	case "random_get":
		return vm.NewNativeFunction(2, false, "wasi.random_get", h.randomGet), nil
	default:
		return vm.Undefined, fmt.Errorf("unsupported WASI import wasi_snapshot_preview1.%s", field)
	}
}

// importBinding is one resolved function import: the native Value to call plus
// its signature (for staging args and knowing whether it yields a result).
type importBinding struct {
	val vm.Value
	sig *FuncType
}

// buildImportBindings resolves every function import against the host, indexed by
// wasm function-import index (imports counted in declaration order, non-func
// imports skipped). Errors if any function import is not a supported WASI call.
func buildImportBindings(m *Module, host *wasiHost) ([]importBinding, error) {
	binds := make([]importBinding, 0, m.ImportedFuncCount)
	for i := range m.Imports {
		im := &m.Imports[i]
		if im.Kind != ImportFunc {
			continue
		}
		if int(im.TypeIndex) >= len(m.Types) {
			return nil, fmt.Errorf("import %s.%s: type index %d out of range", im.Module, im.Field, im.TypeIndex)
		}
		val, err := host.resolve(im.Module, im.Field)
		if err != nil {
			return nil, err
		}
		binds = append(binds, importBinding{val: val, sig: &m.Types[im.TypeIndex]})
	}
	return binds, nil
}
