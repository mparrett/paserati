(module
  (func $fib (export "fib") (param $n i32) (result i32)
    (local $a i32) (local $b i32) (local $i i32) (local $t i32)
    (local.set $a (i32.const 0))
    (local.set $b (i32.const 1))
    (local.set $i (i32.const 0))
    (block $break
      (loop $cont
        (br_if $break (i32.ge_s (local.get $i) (local.get $n)))
        (local.set $t (i32.add (local.get $a) (local.get $b)))
        (local.set $a (local.get $b))
        (local.set $b (local.get $t))
        (local.set $i (i32.add (local.get $i) (i32.const 1)))
        (br $cont)))
    (local.get $a)))
