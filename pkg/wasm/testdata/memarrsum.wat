(module
  (memory 1)
  (data (i32.const 0) "\01\00\00\00\02\00\00\00\03\00\00\00\04\00\00\00\05\00\00\00")
  (func $arrsum (export "arrsum") (param $n i32) (result i32)
    (local $i i32) (local $acc i32)
    (block $b (loop $l
      (br_if $b (i32.ge_s (local.get $i) (local.get $n)))
      (local.set $acc (i32.add (local.get $acc)
        (i32.load (i32.mul (local.get $i) (i32.const 4)))))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $l)))
    (local.get $acc)))
