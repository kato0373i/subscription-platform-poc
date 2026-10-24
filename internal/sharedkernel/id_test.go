package sharedkernel_test

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	sk "subscription-platform/internal/sharedkernel"
)

// AC-PLT-001
func TestIDTypesAreNotInterchangeable(t *testing.T) {
	if testing.Short() {
		t.Skip("go build を呼ぶので -short では省略")
	}
	out, err := exec.Command("go", "build", "-o", "/dev/null", "./testdata/mixedid").CombinedOutput()
	if err == nil {
		t.Fatal("ID[InvTag] を ID[SubTag] として渡せてしまった")
	}
	if !strings.Contains(string(out), "cannot use inv") {
		t.Fatalf("想定と違うビルドエラー: %s", out)
	}
}

// AC-PLT-002
func TestNewIDIsV7AndMonotonic(t *testing.T) {
	type tag struct{}
	prev := sk.NewID[tag]()
	for range 1000 {
		id := sk.NewID[tag]()
		if id.UUID().Version() != 7 {
			t.Fatalf("version = %d", id.UUID().Version())
		}
		if id.String() <= prev.String() {
			t.Fatalf("単調増加でない: %s <= %s", id, prev)
		}
		prev = id
	}
	if _, err := sk.ParseID[tag]("xyz"); !errors.Is(err, sk.ErrInvalidID) {
		t.Fatalf("err = %v", err)
	}
}

func TestIDTextRoundTripAndScan(t *testing.T) {
	type tag struct{}
	id := sk.NewID[tag]()
	b, _ := id.MarshalText()
	var got sk.ID[tag]
	if err := got.UnmarshalText(b); err != nil || got != id {
		t.Fatalf("round trip: %v %v", got, err)
	}
	var scanned sk.ID[tag]
	if err := scanned.Scan(id.String()); err != nil || scanned != id {
		t.Fatalf("scan: %v %v", scanned, err)
	}
	if v, _ := (sk.ID[tag]{}).Value(); v != nil {
		t.Fatalf("zero value should be NULL, got %v", v)
	}
}

func TestNewOpaqueID(t *testing.T) {
	id := sk.NewOpaqueID("cus", 20)
	if !strings.HasPrefix(id, "cus_") || len(id) != 24 {
		t.Fatalf("id = %q", id)
	}
}
