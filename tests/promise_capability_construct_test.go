package tests

import (
	"testing"

	"github.com/nooga/paserati/pkg/driver"
	"github.com/nooga/paserati/pkg/vm"
)

// TestPromiseCombinatorConstructsCapability guards NewPromiseCapability(C):
// the combinators must CONSTRUCT the `this` constructor, not call it. Calling
// it threw "Class constructor SubPromise cannot be invoked without 'new'", so
// Promise.all/race/any/allSettled were unusable on a Promise subclass.
//
// Type checking is skipped because `class SubPromise extends Promise` is
// rejected by the checker today ("superclass 'Promise' does not have a
// constructor"), which is a separate gap; this test is about runtime semantics.
//
// Test262 does not catch this. Its tests for it - built-ins/Promise/{all,race,
// any,allSettled,try}/ctx-ctor.js and the resolve-throws-iterator-return-*
// family - abort on the throw, and the runner scores the aborted run as a pass.
//
// Only all and race are covered: Promise.any and Promise.allSettled return a
// native promise for an empty iterable via an early return that never consults
// the constructor, so they still answer `Promise` rather than the subclass.
// That is a separate defect from Call-versus-Construct and is not fixed here.
func TestPromiseCombinatorConstructsCapability(t *testing.T) {
	for _, combinator := range []string{"all", "race"} {
		t.Run(combinator, func(t *testing.T) {
			code := `
var callCount = 0;
class SubPromise extends Promise {
  constructor(executor) {
    super(executor);
    callCount += 1;
  }
}
var out;
try {
  var r = Promise.` + combinator + `.call(SubPromise, []);
  out = (r instanceof SubPromise) && callCount > 0 ? "constructed" : "wrong";
} catch (e) {
  out = "threw: " + e.message;
}
out;`
			p := driver.NewPaserati()
			p.SetSkipTypeCheck(true)
			result, errs := p.RunString(code)
			if len(errs) > 0 {
				t.Fatalf("evaluation failed: %v", errs)
			}
			if !result.IsString() {
				t.Fatalf("expected string result, got %s", result.TypeName())
			}
			if got := vm.AsString(result); got != "constructed" {
				t.Errorf("Promise.%s.call(SubPromise, []) = %q, want %q", combinator, got, "constructed")
			}
		})
	}
}
