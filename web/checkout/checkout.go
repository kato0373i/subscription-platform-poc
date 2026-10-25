// Package checkout は最小の申込ページ（HTML 1 枚と、トークン化の JS。payments.md §7.10、憲章 §3）を配る。
// 標準ライブラリだけに依存し、cmd が api の Router に登録する。
package checkout

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// securityHeaders は申込ページの応答に付けるヘッダー。外部のスクリプト・送信先を許さない（NFR-007。fincode の JS は将来ここで許可する）。
func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; connect-src 'self'; style-src 'self'; img-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
}

// Routes はパターンとハンドラの組（GET /checkout・GET /checkout/ はページ、GET /checkout/<file> は静的ファイル）。
func Routes() map[string]http.Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		http.ServeFileFS(w, r, sub, "index.html")
	})
	asset := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + r.PathValue("file")
		files.ServeHTTP(w, r2)
	})
	return map[string]http.Handler{
		"GET /checkout":        page,
		"GET /checkout/{$}":    page,
		"GET /checkout/{file}": asset,
	}
}
