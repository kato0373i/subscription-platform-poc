package main

import (
	"strings"
	"testing"
)

// AC-PLT-035
func TestRejectsDuplicateAndCamelCase(t *testing.T) {
	cases := map[string]file{
		"重複":        {Codes: []entry{{Code: "a_b", Status: 400, Title: "x", Owner: "p"}, {Code: "a_b", Status: 400, Title: "x", Owner: "p"}}},
		"CamelCase": {Codes: []entry{{Code: "NotFound", Status: 404, Title: "x", Owner: "p"}}},
		"2xx":       {Codes: []entry{{Code: "ok_code", Status: 200, Title: "x", Owner: "p"}}},
	}
	for name, f := range cases {
		if _, _, err := Generate(f); err == nil {
			t.Errorf("%s: 生成が成功してしまった", name)
		}
	}
}

// AC-PLT-058
func TestGeneratesCommonCodes(t *testing.T) {
	if err := run("../../../api/errors.yaml", t.TempDir()+"/codes_gen.go", t.TempDir()+"/codes.ts"); err != nil {
		t.Fatal(err)
	}
	g, ts, err := Generate(file{Codes: []entry{{Code: "provider_unavailable", Status: 503, Title: "x", Owner: "payments"}}})
	if err != nil || !strings.Contains(string(g), `ProviderUnavailable Code = "provider_unavailable"`) || !strings.Contains(string(ts), `"provider_unavailable"`) {
		t.Fatalf("go=%s ts=%s err=%v", g, ts, err)
	}
}
