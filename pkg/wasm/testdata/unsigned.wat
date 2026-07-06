(module
  (func $gtu (export "gtu") (param $a i32) (param $b i32) (result i32)
    (i32.gt_u (local.get $a) (local.get $b)))
  (func $divu (export "divu") (param $a i32) (param $b i32) (result i32)
    (i32.div_u (local.get $a) (local.get $b))))
