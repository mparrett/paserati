(module
  (func $sw (export "sw") (param $i i32) (result i32)
    (block $d (block $c2 (block $c1 (block $c0
      (br_table $c0 $c1 $c2 $d (local.get $i)))
      (return (i32.const 100)))
      (return (i32.const 200)))
      (return (i32.const 300)))
    (i32.const 999)))
