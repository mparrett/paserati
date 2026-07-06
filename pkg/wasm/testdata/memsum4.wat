(module
  (memory 1)
  (data (i32.const 0) "\04\00\00\00\03\00\00\00\02\00\00\00\01\00\00\00")
  (func $sum4 (export "sum4") (result i32)
    (i32.add
      (i32.add (i32.load (i32.const 0)) (i32.load offset=4 (i32.const 0)))
      (i32.add (i32.load (i32.const 8)) (i32.load (i32.const 12))))))
