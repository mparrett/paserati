(module
  (memory 1)
  (func $i64rt (export "i64rt") (result i64)
    (i64.store (i32.const 0) (i64.const 0x123456789ABCDEF0))
    (i64.load (i32.const 0)))
  (func $i64add (export "i64add") (result i64)
    (i64.add (i64.const 0x20000000000001) (i64.const 1))))
