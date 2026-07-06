(module
  (func $poly (export "poly") (param $x i32) (result i32)
    ;; 2*x*x + 3*x + 1
    (i32.add
      (i32.add
        (i32.mul (i32.const 2) (i32.mul (local.get $x) (local.get $x)))
        (i32.mul (i32.const 3) (local.get $x)))
      (i32.const 1))))
