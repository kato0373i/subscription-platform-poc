// Command webhook-sink はテスト用の Webhook 受信コンテナ（platform.md §10.3、integration.md §11.2 WS-1〜8）。
// 標準ライブラリだけで書き、本番のビルドには含めない。状態はメモリだけ（再起動で消える）。署名は検証しない（WS-7）。
package main

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rule は WS-4 の指示 1 つ。Times が nil なら無制限。
type Rule struct {
	Status    int    `json:"status,omitempty"`
	DelayMS   int    `json:"delay_ms,omitempty"`
	Hang      bool   `json:"hang,omitempty"`
	Close     bool   `json:"close,omitempty"`
	Location  string `json:"location,omitempty"`
	BodyBytes int    `json:"body_bytes,omitempty"`
	Times     *int   `json:"times,omitempty"`
}

// Record は WS-2 の記録 1 件。
type Record struct {
	SinkID     string              `json:"sink_id"`
	Seq        int                 `json:"seq"`
	ReceivedAt string              `json:"received_at"`
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Headers    map[string][]string `json:"headers"`
	BodyBase64 string              `json:"body_base64"`
	BodyText   string              `json:"body_text"`
	Response   Response            `json:"response"`
}

// Response は返した応答。
type Response struct {
	Status int    `json:"status"`
	Rule   string `json:"rule"`
}

type sink struct {
	rules       []Rule
	used        []int
	records     []Record
	inFlight    int
	maxInFlight int
	notify      chan struct{}
}

// Server は全 sink の状態。
type Server struct {
	mu    sync.Mutex
	sinks map[string]*sink
	seq   int
}

func NewServer() *Server { return &Server{sinks: map[string]*sink{}} }

func (s *Server) get(id string) *sink {
	sk, ok := s.sinks[id]
	if !ok {
		sk = &sink{notify: make(chan struct{})}
		s.sinks[id] = sk
	}
	return sk
}

// pick は次に使う規則を返す（使い切ったら nil = 200 ok）。
func (sk *sink) pick() (*Rule, string) {
	for i := range sk.rules {
		r := &sk.rules[i]
		if r.Times == nil || sk.used[i] < *r.Times {
			sk.used[i]++
			b, _ := json.Marshal(r)
			return r, string(b)
		}
	}
	return nil, "default"
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_control/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("PUT /_control/sinks/{id}/behavior", s.setBehavior)
	mux.HandleFunc("GET /_control/sinks/{id}/requests", s.requests)
	mux.HandleFunc("GET /_control/sinks/{id}/stats", s.stats)
	mux.HandleFunc("DELETE /_control/sinks/{id}", s.deleteSink)
	mux.HandleFunc("/hooks/", s.receive)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) setBehavior(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Rules []Rule `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	sk := s.get(r.PathValue("id"))
	sk.rules, sk.used = body.Rules, make([]int, len(body.Rules))
	s.mu.Unlock()
	writeJSON(w, 200, map[string]int{"rules": len(body.Rules)})
}

func (s *Server) requests(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	wait := 0
	if v := r.URL.Query().Get("wait_ms"); v != "" {
		wait, _ = strconv.Atoi(v)
	} else if v := r.URL.Query().Get("wait"); v != "" {
		sec, _ := strconv.Atoi(v)
		wait = sec * 1000
	}
	wait = min(max(wait, 0), 10000)
	deadline := time.Now().Add(time.Duration(wait) * time.Millisecond)
	for {
		s.mu.Lock()
		sk := s.get(id)
		var out []Record
		for _, rec := range sk.records {
			if rec.Seq > after {
				out = append(out, rec)
			}
		}
		ch := sk.notify
		s.mu.Unlock()
		if len(out) > 0 || time.Now().After(deadline) {
			if out == nil {
				out = []Record{}
			}
			writeJSON(w, 200, map[string]any{"requests": out})
			return
		}
		select {
		case <-ch:
		case <-time.After(time.Until(deadline)):
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sk := s.get(r.PathValue("id"))
	resp := map[string]int{"count": len(sk.records), "max_in_flight": sk.maxInFlight}
	s.mu.Unlock()
	writeJSON(w, 200, resp)
}

func (s *Server) deleteSink(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	delete(s.sinks, r.PathValue("id"))
	s.mu.Unlock()
	w.WriteHeader(204)
}

func (s *Server) receive(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/hooks/")
	id, _, _ := strings.Cut(rest, "/")
	if id == "" {
		writeJSON(w, 404, map[string]string{"error": "sink_id is required"})
		return
	}
	body := make([]byte, 0)
	if r.Body != nil {
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Body.Read(buf)
			body = append(body, buf[:n]...)
			if err != nil {
				break
			}
		}
	}
	s.mu.Lock()
	sk := s.get(id)
	sk.inFlight++
	sk.maxInFlight = max(sk.maxInFlight, sk.inFlight)
	rule, ruleDesc := sk.pick()
	s.seq++
	rec := Record{
		SinkID: id, Seq: s.seq, ReceivedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Method: r.Method, Path: r.URL.Path, Headers: r.Header.Clone(),
		BodyBase64: base64.StdEncoding.EncodeToString(body), BodyText: string(body),
	}
	s.mu.Unlock()

	status := respond(w, r, rule)
	rec.Response = Response{Status: status, Rule: ruleDesc}

	s.mu.Lock()
	sk = s.get(id)
	sk.inFlight--
	sk.records = append(sk.records, rec)
	close(sk.notify)
	sk.notify = make(chan struct{})
	s.mu.Unlock()
}

// respond は規則に従って応答し、返したステータスを返す（hang・close は 0）。
func respond(w http.ResponseWriter, r *http.Request, rule *Rule) int {
	if rule == nil {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
		return 200
	}
	if rule.DelayMS > 0 {
		select {
		case <-time.After(time.Duration(rule.DelayMS) * time.Millisecond):
		case <-r.Context().Done():
			return 0
		}
	}
	if rule.Hang {
		<-r.Context().Done() // 送信側のタイムアウトで切られるまで応答しない
		return 0
	}
	if rule.Close {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
		return 0
	}
	status := rule.Status
	if status == 0 {
		status = 200
	}
	if rule.Location != "" {
		w.Header().Set("Location", rule.Location)
	}
	w.WriteHeader(status)
	if rule.BodyBytes > 0 {
		_, _ = w.Write([]byte(strings.Repeat("x", rule.BodyBytes)))
	} else {
		_, _ = w.Write([]byte(http.StatusText(status)))
	}
	return status
}

func main() {
	addr := os.Getenv("SINK_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{Addr: addr, Handler: NewServer().Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("webhook-sink listening on %s", addr) //nolint:forbidigo,gosec // テスト用のツール
	log.Fatal(srv.ListenAndServe())                  //nolint:forbidigo
}
