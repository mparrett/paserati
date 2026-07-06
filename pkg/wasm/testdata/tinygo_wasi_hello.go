// Source for tinygo_wasi_hello.wasm — a real TinyGo WASI binary used to test
// that the decoder parses a genuine Go-toolchain module (imports, tables,
// globals, elems, bulk memory, i64/float widths). Codegen does not yet lower it.
//
// Rebuild:
//   tinygo build -target=wasi -no-debug -opt=z -o tinygo_wasi_hello.wasm .
// (tinygo 0.41.x). Ground truth: `wasmtime tinygo_wasi_hello.wasm` prints the line.

package main

func main() {
	println("hello from tinygo wasi")
}
