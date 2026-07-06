(module
  (func $max (export "max") (param $a i32) (param $b i32) (result i32)
    (local $m i32)
    (local.set $m (local.get $b))
    (if (i32.gt_s (local.get $a) (local.get $b))
      (then (local.set $m (local.get $a))))
    (local.get $m)))
