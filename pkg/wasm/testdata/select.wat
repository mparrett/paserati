(module
  (func $sel (export "sel") (param $a i32) (param $b i32) (param $c i32) (result i32)
    (select (local.get $a) (local.get $b) (local.get $c))))
