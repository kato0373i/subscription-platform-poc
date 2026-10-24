package config_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"subscription-platform/internal/platform/config"
)

func envOf(m map[string]string) config.Getenv { return func(k string) string { return m[k] } }

func base() map[string]string {
	return map[string]string{
		"APP_ENV":          "local",
		"DATABASE_URL":     "postgres://x",
		"QUEUE_URL_PREFIX": "http://localhost:4566/000000000000/",
		"CURSOR_KEY":       "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}
}

// AC-PLT-045
func TestMissingRequiredAreListedTogether(t *testing.T) {
	m := base()
	delete(m, "DATABASE_URL")
	delete(m, "CURSOR_KEY")
	_, err := config.LoadFrom("api", envOf(m), true)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "CURSOR_KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestMustLoadExitsNonZero(t *testing.T) {
	if os.Getenv("SP_CONFIG_CHILD") == "1" {
		config.MustLoad("api")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMustLoadExitsNonZero")
	cmd.Env = append(os.Environ(), "SP_CONFIG_CHILD=1", "DATABASE_URL=", "CURSOR_KEY=")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "DATABASE_URL") || !strings.Contains(string(out), "CURSOR_KEY") {
		t.Fatalf("err = %v, out = %s", err, out)
	}
}

// AC-PLT-042（設定の部分）、AC-PLT-084
func TestRejectedCombinations(t *testing.T) {
	cases := []struct {
		name     string
		mod      map[string]string
		devtools bool
	}{
		{"devtools を prod で", map[string]string{"APP_ENV": "prod", "LOG_SINKS": "stdout", "LOG_HTTP_BODY": "off"}, true},
		{"タグなしで AUTH_MODE=dev", map[string]string{"AUTH_MODE": "dev"}, false},
		{"prod で本文を記録", map[string]string{"APP_ENV": "prod", "AUTH_MODE": "cognito", "LOG_SINKS": "stdout", "LOG_HTTP_BODY": "redacted"}, false},
		{"prod でファイルに出力", map[string]string{"APP_ENV": "prod", "LOG_SINKS": "stdout,file", "LOG_HTTP_BODY": "off"}, false},
		{"local 以外で insecure hosts", map[string]string{"APP_ENV": "test", "WEBHOOK_INSECURE_ALLOWED_HOSTS": "webhook-sink"}, true},
		{"CURSOR_KEY が短い", map[string]string{"CURSOR_KEY": "c2hvcnQ="}, true},
	}
	for _, tc := range cases {
		m := base()
		for k, v := range tc.mod {
			m[k] = v
		}
		if _, err := config.LoadFrom("worker-billing", envOf(m), tc.devtools); err == nil {
			t.Errorf("%s: 起動を拒否しなかった", tc.name)
		}
	}
}

func TestDefaults(t *testing.T) {
	c, err := config.LoadFrom("api", envOf(base()), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthMode != "dev" || c.DBMaxConns != 20 || strings.Join(c.Log.Sinks, ",") != "stdout,file" || c.Log.HTTPBody != "redacted" {
		t.Fatalf("config = %+v", c)
	}
	if strings.Join(c.WebhookInsecureAllowedHosts, ",") != "localhost,127.0.0.1" {
		t.Fatalf("hosts = %v", c.WebhookInsecureAllowedHosts)
	}
}
