(module
  (memory 1)
  (func $fillcopy (export "fillcopy") (result i32)
    (memory.fill (i32.const 0) (i32.const 0xAB) (i32.const 4))
    (memory.copy (i32.const 8) (i32.const 0) (i32.const 4))
    (i32.load (i32.const 8))))
