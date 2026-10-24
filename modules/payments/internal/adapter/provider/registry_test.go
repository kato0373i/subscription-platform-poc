package provider_test

import (
	"errors"
	"testing"

	"golang.org/x/tools/go/packages"

	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/adapter/provider/stub"
)

// AC-PAY-030: 二重登録は panic、有効化されていない手段は ErrUnsupportedMethod
func TestRegistry(t *testing.T) {
	r, err := provider.NewRegistry("card:stub")
	if err != nil {
		t.Fatal(err)
	}
	r.Register(stub.New("stub", nil, nil, "s"))
	func() {
		defer func() {
			if recover() == nil {
				t.Error("二重登録で panic しない")
			}
		}()
		r.Register(stub.New("stub", nil, nil, "s"))
	}()
	if _, err := r.ForRegistration(c.MethodCard, "stub"); err != nil {
		t.Errorf("有効な組: %v", err)
	}
	if _, err := r.ForRegistration(c.MethodCard, "fincode"); !errors.Is(err, c.ErrUnsupportedMethod) {
		t.Errorf("未設定の代行: %v", err)
	}
	if _, err := r.ForRegistration("bank_debit", "stub"); !errors.Is(err, c.ErrUnsupportedMethod) {
		t.Errorf("未設定の手段: %v", err)
	}
	if _, err := provider.NewRegistry("card:stub,card:fincode"); err == nil {
		t.Error("1 つの手段に 2 つの代行を有効化できた")
	}
}

// AC-PAY-031: 別の ProviderID の StubAdapter も同じように解決でき、app・domain・contracts は具体的なアダプターを import しない
func TestProviderNeutral(t *testing.T) {
	r, _ := provider.NewRegistry("card:stub")
	r.Register(stub.New("stub", nil, nil, "s"))
	r.Register(stub.New("stub_alt", nil, nil, "s"))
	for _, name := range []string{"stub", "stub_alt"} {
		p, err := r.Get(provider.Key{Method: c.MethodCard, Provider: provider.ProviderID(name)})
		if err != nil || p.Key().Provider != provider.ProviderID(name) {
			t.Errorf("%s: %v", name, err)
		}
		if _, ok := provider.Capability[provider.MethodRegistrar](p); !ok {
			t.Errorf("%s: 任意の能力が取り出せない", name)
		}
	}
	if testing.Short() {
		return
	}
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedImports, Dir: "../../.."},
		"./internal/app", "./internal/domain", "./contracts")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		for imp := range p.Imports {
			if imp == "subscription-platform/modules/payments/internal/adapter/provider/stub" || imp == "subscription-platform/modules/payments/internal/adapter/provider/fincode" {
				t.Errorf("%s が具体的なアダプター %s を import している", p.PkgPath, imp)
			}
		}
	}
}
