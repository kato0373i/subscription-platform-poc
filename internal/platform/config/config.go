// Package config は環境変数を読み、起動時に全項目を検証する。不足や矛盾はまとめて返す（platform.md §8.5）。
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"subscription-platform/internal/platform/observability"
)

// Version は -ldflags "-X subscription-platform/internal/platform/config.Version=<commit>" で埋め込む。
var Version = "dev"

type CognitoPool struct{ Issuer, ClientID string }

type Config struct {
	Service string
	AppEnv  string

	DatabaseURL string
	DBMaxConns  int32
	DBMode      string

	AWSRegion      string
	AWSEndpointURL string
	EventBusName   string
	QueueURLPrefix string
	MessagingMode  string // eventbridge | direct
	SchedulerMode  string // aws | internal

	AuthMode        string // dev | cognito
	CognitoConsumer CognitoPool
	CognitoStaff    CognitoPool
	CognitoOperator CognitoPool
	DevJWTKeyPath   string

	CursorKey []byte

	HTTPAddr   string
	HealthAddr string

	WebhookInsecureAllowedHosts []string
	MailMode                    string

	Log       observability.LogConfig
	SlowQuery time.Duration

	CatalogCouponReservationTTL time.Duration // 放置されたクーポンの予約を解放するまでの時間（catalog.md §8）
	BillingCollectionLease      time.Duration // 回収要求のリース（billing.md §8。結果が届かなければ同じ collection_seq で再送する）

	PaymentsMethods   string // 新規登録に使う手段と代行（payments.md §4.4。例 "card:stub"）
	StubWebhookSecret string // StubAdapter の Webhook の署名鍵（devtools のみ）
	FincodeAPIBaseURL string
	FincodeSecretKey  string
	FincodePublicKey  string
	ShutdownSec       int
}

// Getenv は環境変数の取得元（テストで差し替える）。
type Getenv func(string) string

// Load は service（api / worker-billing / worker-integration / migrate）の設定を読む。
func Load(service string) (Config, error) { return LoadFrom(service, os.Getenv, DevtoolsBuild) }

// LoadFrom は取得元と devtools ビルドかを指定して読む。
func LoadFrom(service string, env Getenv, devtools bool) (Config, error) {
	var errs []error
	get := func(k, def string) string {
		if v := strings.TrimSpace(env(k)); v != "" {
			return v
		}
		return def
	}
	oneOf := func(k, def string, allowed ...string) string {
		v := get(k, def)
		if !slices.Contains(allowed, v) {
			errs = append(errs, fmt.Errorf("%s: %q is not one of %v", k, v, allowed))
		}
		return v
	}
	required := func(k string) string {
		v := get(k, "")
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", k))
		}
		return v
	}

	c := Config{Service: service}
	c.AppEnv = oneOf("APP_ENV", "local", "local", "test", "dev", "stg", "prod")
	localEnv := c.AppEnv == "local" || c.AppEnv == "test"
	if devtools && !localEnv {
		errs = append(errs, fmt.Errorf("devtools build must not run with APP_ENV=%s", c.AppEnv))
	}

	c.DatabaseURL = required("DATABASE_URL")
	defConns := "10"
	if service == "api" {
		defConns = "20"
	}
	if n, err := strconv.Atoi(get("DB_MAX_CONNS", defConns)); err != nil || n < 1 {
		errs = append(errs, fmt.Errorf("DB_MAX_CONNS: invalid %q", get("DB_MAX_CONNS", defConns)))
	} else {
		c.DBMaxConns = int32(n) //nolint:gosec // 範囲は上で検査済み
	}
	c.DBMode = oneOf("DB_MODE", "rds", "rds", "container")

	c.AWSRegion = get("AWS_REGION", "ap-northeast-1")
	c.AWSEndpointURL = get("AWS_ENDPOINT_URL", "")
	c.EventBusName = get("EVENT_BUS_NAME", "sp-events")
	c.QueueURLPrefix = get("QUEUE_URL_PREFIX", "")
	if service != "migrate" && c.QueueURLPrefix == "" {
		errs = append(errs, errors.New("QUEUE_URL_PREFIX is required"))
	}
	c.MessagingMode = oneOf("MESSAGING_MODE", "eventbridge", "eventbridge", "direct")
	c.SchedulerMode = oneOf("SCHEDULER_MODE", "aws", "aws", "internal")

	defAuth := "cognito"
	if devtools {
		defAuth = "dev"
	}
	c.AuthMode = oneOf("AUTH_MODE", defAuth, "dev", "cognito")
	if c.AuthMode == "dev" && !devtools {
		errs = append(errs, errors.New("AUTH_MODE=dev requires a devtools build"))
	}
	c.DevJWTKeyPath = get("DEV_JWT_KEY_PATH", "deploy/local/dev-jwt.pem")
	if c.AuthMode == "cognito" && service == "api" {
		for _, p := range []struct {
			name string
			dst  *CognitoPool
		}{{"CONSUMER", &c.CognitoConsumer}, {"STAFF", &c.CognitoStaff}, {"OPERATOR", &c.CognitoOperator}} {
			p.dst.Issuer = required("COGNITO_" + p.name + "_ISSUER")
			p.dst.ClientID = required("COGNITO_" + p.name + "_CLIENT_ID")
		}
	}

	if service != "migrate" {
		if raw := required("CURSOR_KEY"); raw != "" {
			k, err := base64.StdEncoding.DecodeString(raw)
			if err != nil || len(k) != 32 {
				errs = append(errs, errors.New("CURSOR_KEY must be 32 bytes encoded in base64"))
			}
			c.CursorKey = k
		}
	}

	c.HTTPAddr = get("HTTP_ADDR", ":8080")
	c.HealthAddr = get("HEALTH_ADDR", ":8081")

	if v := env("WEBHOOK_INSECURE_ALLOWED_HOSTS"); v != "" {
		if c.AppEnv != "local" {
			errs = append(errs, errors.New("WEBHOOK_INSECURE_ALLOWED_HOSTS is only allowed when APP_ENV=local"))
		}
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				c.WebhookInsecureAllowedHosts = append(c.WebhookInsecureAllowedHosts, h)
			}
		}
	} else if c.AppEnv == "local" {
		c.WebhookInsecureAllowedHosts = []string{"localhost", "127.0.0.1"}
	}
	c.MailMode = oneOf("MAIL_MODE", "ses", "ses", "log")

	defSinks, defBody := "stdout", "off"
	if localEnv {
		defSinks, defBody = "stdout,file", "redacted"
	}
	c.Log = observability.LogConfig{
		Service:  get("SERVICE_NAME", service),
		Version:  get("SERVICE_VERSION", Version),
		Env:      c.AppEnv,
		Sinks:    splitList(get("LOG_SINKS", defSinks)),
		Dir:      get("LOG_DIR", ".local/logs"),
		Level:    get("LOG_LEVEL", "info"),
		Format:   get("LOG_FORMAT", "json"),
		HTTPBody: get("LOG_HTTP_BODY", defBody),
	}
	if err := c.Log.Validate(); err != nil {
		errs = append(errs, err)
	}
	ms, err := strconv.Atoi(get("DB_SLOW_QUERY_MS", "200"))
	if err != nil || ms < 1 {
		errs = append(errs, fmt.Errorf("DB_SLOW_QUERY_MS: invalid %q", get("DB_SLOW_QUERY_MS", "200")))
	}
	c.SlowQuery = time.Duration(ms) * time.Millisecond
	defMethods := ""
	if devtools {
		defMethods = "card:stub"
	}
	c.PaymentsMethods = get("PAYMENTS_METHODS", defMethods)
	c.StubWebhookSecret = get("STUB_WEBHOOK_SECRET", "")
	if devtools && c.StubWebhookSecret == "" {
		c.StubWebhookSecret = "local-stub-webhook-secret" //nolint:gosec // devtools ビルドだけの既定値（ローカルの StubAdapter 用）
	}
	c.FincodeAPIBaseURL, c.FincodeSecretKey, c.FincodePublicKey = get("FINCODE_API_BASE_URL", ""), get("FINCODE_SECRET_KEY", ""), get("FINCODE_PUBLIC_KEY", "")
	if d, err := time.ParseDuration(get("CATALOG_COUPON_RESERVATION_TTL", "24h")); err != nil || d <= 0 {
		errs = append(errs, fmt.Errorf("CATALOG_COUPON_RESERVATION_TTL: invalid %q", get("CATALOG_COUPON_RESERVATION_TTL", "24h")))
	} else {
		c.CatalogCouponReservationTTL = d
	}
	if d, err := time.ParseDuration(get("BILLING_COLLECTION_LEASE", "6h")); err != nil || d <= 0 {
		errs = append(errs, fmt.Errorf("BILLING_COLLECTION_LEASE: invalid %q", get("BILLING_COLLECTION_LEASE", "6h")))
	} else {
		c.BillingCollectionLease = d
	}

	if len(errs) > 0 {
		return c, &Error{Errs: errs}
	}
	return c, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Error は設定の誤りをまとめて持つ。
type Error struct{ Errs []error }

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("invalid configuration:")
	for _, err := range e.Errs {
		b.WriteString("\n  - ")
		b.WriteString(err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() []error { return e.Errs }

// MustLoad は設定を読み、誤りがあれば標準エラーに全件を出して終了コード 2 で終わる（AC-PLT-045）。
func MustLoad(service string) Config {
	c, err := Load(service)
	if err != nil {
		fmt.Fprintln(os.Stderr, err) //nolint:forbidigo // ログの仕組みを作る前の起動時エラー
		os.Exit(2)
	}
	return c
}
