(module
  (func $sum (export "sum") (param $n i32) (result i32)
    (local $i i32) (local $acc i32)
    (block $break
      (loop $cont
        (br_if $break (i32.gt_s (local.get $i) (local.get $n)))
        (local.set $acc (i32.add (local.get $acc) (local.get $i)))
        (local.set $i (i32.add (local.get $i) (i32.const 1)))
        (br $cont)))
    (local.get $acc)))
