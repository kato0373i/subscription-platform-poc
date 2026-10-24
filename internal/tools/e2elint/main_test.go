package main

import (
	"strings"
	"testing"
)

// AC-PLT-051・AC-PLT-068（lint の部分）
func TestCheck(t *testing.T) {
	errs := Check("testdata/tests", []string{"getThing", "listThings"})
	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"x/bad.spec.ts: clock.advance", "x/bad.spec.ts: waitForTimeout", "listThings は x-sp-isolation: tested"} {
		if !strings.Contains(joined, want) {
			t.Errorf("検出されなかった: %s\n%s", want, joined)
		}
	}
	if len(errs) != 3 {
		t.Errorf("errs = %v", msgs)
	}
}

func TestRepository(t *testing.T) {
	ops, err := testedOperations()
	if err != nil {
		t.Fatal(err)
	}
	if errs := Check("../../../e2e/tests", ops); len(errs) > 0 {
		t.Fatal(errs)
	}
}
