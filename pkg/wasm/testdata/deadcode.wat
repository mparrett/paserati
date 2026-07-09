(module
  ;; br 0 with no enclosing block == branch to the function-implicit block == return
  (func (export "br_return") (param i32) (result i32)
    local.get 0
    br 0)
  ;; br 1 from inside one block == function-level branch (the func287 pattern)
  (func (export "br_return_nested") (param i32) (result i32)
    (block (result i32)
      local.get 0
      i32.const 1
      i32.add
      br 1))
  ;; dead code after return, containing a nested block (must be skipped, not error)
  (func (export "deadcode") (result i32)
    i32.const 42
    return
    block
      i32.const 99
      drop
    end
    i32.const 7)
  ;; dead code after return with unreachable nested inside dead code
  (func (export "dead_after_unreachable") (param i32) (result i32)
    local.get 0
    return
    block
      unreachable
      i32.const 123
      drop
    end
    i32.const 456))
