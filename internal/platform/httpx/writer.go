package httpx

import (
	"bytes"
	"net/http"
)

// responseRecorder は一番外側で応答の status・大きさ・本文の先頭を記録する（http.request.completed 用）。
type responseRecorder struct {
	http.ResponseWriter
	status  int
	bytes   int
	body    *bytes.Buffer // LOG_HTTP_BODY=redacted のときだけ
	errCode string
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.body != nil && r.body.Len() < 64<<10 {
		r.body.Write(b)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err //nolint:wrapcheck
}

func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// bufferWriter は応答をいったん溜める（監査の記録・Idempotency の保存の後で返すため）。
type bufferWriter struct {
	header    http.Header
	status    int
	body      bytes.Buffer
	transient bool
	errCode   string
}

func newBufferWriter() *bufferWriter { return &bufferWriter{header: http.Header{}} }

func (b *bufferWriter) Header() http.Header { return b.header }

func (b *bufferWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (b *bufferWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p) //nolint:wrapcheck
}

func (b *bufferWriter) code() int {
	if b.status == 0 {
		return http.StatusOK
	}
	return b.status
}

func (b *bufferWriter) markTransient() { b.transient = true }

// flushTo は溜めた応答を w に書く。
func (b *bufferWriter) flushTo(w http.ResponseWriter) {
	for k, vs := range b.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if b.errCode != "" {
		if rec, ok := w.(*responseRecorder); ok {
			rec.errCode = b.errCode
		}
	}
	w.WriteHeader(b.code())
	_, _ = w.Write(b.body.Bytes())
}

type transientMarker interface{ markTransient() }

// MarkTransient はこの応答を Idempotency に保存しない（例: 申込の 202）。
func MarkTransient(w http.ResponseWriter) {
	for {
		if t, ok := w.(transientMarker); ok {
			t.markTransient()
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}
