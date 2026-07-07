package wasm

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

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
	stdin   io.Reader
	args    []string // argv exposed via args_get; nil → just the program name
	environ []string // "K=V" strings exposed via environ_get

	nowNanos func() int64 // clock_time_get source; injectable for tests

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
	return &wasiHost{
		getData:  getData,
		stdout:   stdout,
		stderr:   stderr,
		stdin:    os.Stdin,
		args:     []string{"lg"},
		nowNanos: func() int64 { return time.Now().UnixNano() },
	}
}

// WASI errno values this subset uses.
const (
	wasiESuccess = 0
	wasiEBadf    = 8  // __WASI_ERRNO_BADF — no such fd / end of preopens
	wasiEFault   = 21 // __WASI_ERRNO_FAULT — an out-of-bounds pointer
	wasiENosys   = 52 // __WASI_ERRNO_NOSYS — unimplemented capability
)

// fd numbers for the standard streams.
const (
	fdStdin  = 0
	fdStdout = 1
	fdStderr = 2
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

// putU32/putU64 write a little-endian integer to memory, bounds-checked.
func putU32(data []byte, off, v uint32) bool {
	if !inBounds(data, off, 4) {
		return false
	}
	binary.LittleEndian.PutUint32(data[off:], v)
	return true
}

func putU64(data []byte, off uint32, v uint64) bool {
	if !inBounds(data, off, 8) {
		return false
	}
	binary.LittleEndian.PutUint64(data[off:], v)
	return true
}

// fdRead implements fd_read(fd, iovs, iovs_len, nread) — reads from stdin (fd 0)
// into the iovec buffers, storing the count at *nread. EOF yields 0 bytes / errno
// 0. Other fds are unreadable in this subset.
func (h *wasiHost) fdRead(args []vm.Value) (vm.Value, error) {
	fd := int32(args[0].ToFloat())
	iovs := asU32Arg(args[1])
	iovsLen := asU32Arg(args[2])
	nread := asU32Arg(args[3])
	if fd != fdStdin || h.stdin == nil {
		return vm.Number(wasiEBadf), nil
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
		n, err := h.stdin.Read(data[buf : buf+length])
		total += uint32(n)
		if err != nil || n < int(length) {
			break // EOF or short read: stop gathering (io.EOF is not an error here)
		}
	}
	if !putU32(data, nread, total) {
		return vm.Number(wasiEFault), nil
	}
	return vm.Number(wasiESuccess), nil
}

// argsSizesGet / argsGet expose h.args; environSizesGet / environGet expose
// h.environ. Both follow the two-call WASI pattern: sizes first, then the buffer.
func (h *wasiHost) vecSizesGet(vec []string, args []vm.Value) (vm.Value, error) {
	data := h.getData()
	countPtr := asU32Arg(args[0])
	bufSizePtr := asU32Arg(args[1])
	var bufSize uint32
	for _, s := range vec {
		bufSize += uint32(len(s)) + 1 // NUL terminator
	}
	if !putU32(data, countPtr, uint32(len(vec))) || !putU32(data, bufSizePtr, bufSize) {
		return vm.Number(wasiEFault), nil
	}
	return vm.Number(wasiESuccess), nil
}

func (h *wasiHost) vecGet(vec []string, args []vm.Value) (vm.Value, error) {
	data := h.getData()
	ptrs := asU32Arg(args[0]) // array of u32 pointers
	buf := asU32Arg(args[1])  // NUL-separated string bytes
	for i, s := range vec {
		if !putU32(data, ptrs+uint32(i)*4, buf) {
			return vm.Number(wasiEFault), nil
		}
		if !inBounds(data, buf, uint32(len(s))+1) {
			return vm.Number(wasiEFault), nil
		}
		copy(data[buf:], s)
		data[buf+uint32(len(s))] = 0
		buf += uint32(len(s)) + 1
	}
	return vm.Number(wasiESuccess), nil
}

// clockTimeGet implements clock_time_get(id, precision, time) — writes the host
// clock (nanoseconds) at *time. All clock ids share one source here.
func (h *wasiHost) clockTimeGet(args []vm.Value) (vm.Value, error) {
	timePtr := asU32Arg(args[2])
	if !putU64(h.getData(), timePtr, uint64(h.nowNanos())) {
		return vm.Number(wasiEFault), nil
	}
	return vm.Number(wasiESuccess), nil
}

// fdFdstatGet implements fd_fdstat_get(fd, stat) — reports the standard streams as
// character devices with full rights; unknown fds are bad. The fdstat struct is 24
// bytes: filetype u8 @0, flags u16 @2, rights_base u64 @8, rights_inheriting u64 @16.
func (h *wasiHost) fdFdstatGet(args []vm.Value) (vm.Value, error) {
	fd := int32(args[0].ToFloat())
	statPtr := asU32Arg(args[1])
	if fd != fdStdin && fd != fdStdout && fd != fdStderr {
		return vm.Number(wasiEBadf), nil
	}
	data := h.getData()
	if !inBounds(data, statPtr, 24) {
		return vm.Number(wasiEFault), nil
	}
	for i := uint32(0); i < 24; i++ {
		data[statPtr+i] = 0
	}
	data[statPtr] = 2 // __WASI_FILETYPE_CHARACTER_DEVICE
	putU64(data, statPtr+8, ^uint64(0))
	putU64(data, statPtr+16, ^uint64(0))
	return vm.Number(wasiESuccess), nil
}

// pollOneoff implements poll_oneoff(in, out, nsub, nevents) minimally: every
// subscription is reported as immediately ready with no error. That's enough for
// a line-oriented REPL polling stdin, and treats clock waits as already elapsed.
// Subscription = 48 bytes (userdata u64 @0, tag u8 @8); event = 32 bytes
// (userdata u64 @0, error u16 @8, type u8 @10, fd_readwrite.nbytes u64 @16).
func (h *wasiHost) pollOneoff(args []vm.Value) (vm.Value, error) {
	in := asU32Arg(args[0])
	out := asU32Arg(args[1])
	nsub := asU32Arg(args[2])
	neventsPtr := asU32Arg(args[3])
	data := h.getData()
	for i := uint32(0); i < nsub; i++ {
		sub := in + i*48
		ev := out + i*32
		if !inBounds(data, sub, 48) || !inBounds(data, ev, 32) {
			return vm.Number(wasiEFault), nil
		}
		for k := uint32(0); k < 32; k++ {
			data[ev+k] = 0
		}
		copy(data[ev:ev+8], data[sub:sub+8]) // userdata echoes back
		data[ev+10] = data[sub+8]            // event type = subscription tag
		putU64(data, ev+16, 1)               // fd_readwrite.nbytes: claim 1 byte ready
	}
	if !putU32(data, neventsPtr, nsub) {
		return vm.Number(wasiEFault), nil
	}
	return vm.Number(wasiESuccess), nil
}

// wasiExit is the sentinel proc_exit throws to unwind _start.
type wasiExit struct{ code int32 }

func (e wasiExit) Error() string { return fmt.Sprintf("wasi: proc_exit(%d)", e.code) }

// resolve maps an import's (module, field) to its native helper Value. Implemented
// imports get a real helper; every other wasi_snapshot_preview1 function binds to
// a stub that returns ENOSYS *when called*, so a module that merely links the full
// WASI surface (e.g. TinyGo's os package) still compiles and runs until it
// actually needs an unimplemented capability. A non-WASI import module is a hard
// error — we can't stub something whose contract we don't know.
func (h *wasiHost) resolve(module, field string, sig *FuncType) (vm.Value, error) {
	if module != "wasi_snapshot_preview1" {
		return vm.Undefined, fmt.Errorf("unsupported import module %q", module)
	}
	arity := len(sig.Params)
	fn := func(f func([]vm.Value) (vm.Value, error)) vm.Value {
		return vm.NewNativeFunction(arity, false, "wasi."+field, f)
	}
	switch field {
	case "fd_write":
		return fn(h.fdWrite), nil
	case "fd_read":
		return fn(h.fdRead), nil
	case "proc_exit":
		return fn(h.procExit), nil
	case "random_get":
		return fn(h.randomGet), nil
	case "clock_time_get":
		return fn(h.clockTimeGet), nil
	case "args_sizes_get":
		return fn(func(a []vm.Value) (vm.Value, error) { return h.vecSizesGet(h.args, a) }), nil
	case "args_get":
		return fn(func(a []vm.Value) (vm.Value, error) { return h.vecGet(h.args, a) }), nil
	case "environ_sizes_get":
		return fn(func(a []vm.Value) (vm.Value, error) { return h.vecSizesGet(h.environ, a) }), nil
	case "environ_get":
		return fn(func(a []vm.Value) (vm.Value, error) { return h.vecGet(h.environ, a) }), nil
	case "fd_fdstat_get":
		return fn(h.fdFdstatGet), nil
	case "poll_oneoff":
		return fn(h.pollOneoff), nil
	case "fd_prestat_get":
		// No preopened directories: EBADF ends wasi-libc's preopen enumeration.
		return fn(func([]vm.Value) (vm.Value, error) { return vm.Number(wasiEBadf), nil }), nil
	default:
		// Unimplemented (mostly filesystem): trap with ENOSYS only if called.
		return fn(func([]vm.Value) (vm.Value, error) { return vm.Number(wasiENosys), nil }), nil
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
		sig := &m.Types[im.TypeIndex]
		val, err := host.resolve(im.Module, im.Field, sig)
		if err != nil {
			return nil, err
		}
		binds = append(binds, importBinding{val: val, sig: sig})
	}
	return binds, nil
}
