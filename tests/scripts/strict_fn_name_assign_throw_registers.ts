// skip-typecheck
// The "throw new TypeError(...)" sequence for assigning to a function's own
// name loaded its message into callee+1 without allocating that register.
// With the temporaries from `n++` still live, callee+1 sat one past the frame's
// register file and the VM panicked with "index out of range" instead of
// throwing (test262 generators/named-strict-error-reassign-fn-name-in-body).
"use strict";
var n = 0;
var g = function* fn() {
  n++;
  fn = 1;
};
var r = "no throw";
try {
  g().next();
} catch (e) {
  r = e.constructor.name + ":" + n;
}
r;
// expect: TypeError:1
