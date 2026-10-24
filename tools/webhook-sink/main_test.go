package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSinkRulesAndRecords(t *testing.T) {
	srv := httptest.NewServer(NewServer().Handler())
	defer srv.Close()
	put := func(body string) {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/_control/sinks/s1/behavior", strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("behavior: %v %v", resp, err)
		}
	}
	put(`{"rules":[{"status":500,"times":2},{"status":302,"location":"https://x","times":1}]}`)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var got []int
	for range 4 {
		resp, err := client.Post(srv.URL+"/hooks/s1/extra/path", "application/json", strings.NewReader(`{"a":1}`))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, resp.StatusCode)
	}
	if want := []int{500, 500, 302, 200}; !equal(got, want) {
		t.Fatalf("statuses = %v", got)
	}
	resp, _ := http.Get(srv.URL + "/_control/sinks/s1/requests?after=1")
	var body struct{ Requests []Record }
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Requests) != 3 || body.Requests[0].BodyText != `{"a":1}` || body.Requests[0].Path != "/hooks/s1/extra/path" || body.Requests[2].Response.Status != 200 {
		t.Fatalf("records = %+v", body.Requests)
	}
}

func TestLongPollingAndMaxInFlight(t *testing.T) {
	srv := httptest.NewServer(NewServer().Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/_control/sinks/s2/behavior", strings.NewReader(`{"rules":[{"delay_ms":300,"status":200}]}`))
	_, _ = http.DefaultClient.Do(req)
	done := make(chan []Record)
	go func() {
		resp, _ := http.Get(srv.URL + "/_control/sinks/s2/requests?wait_ms=5000")
		var b struct{ Requests []Record }
		_ = json.NewDecoder(resp.Body).Decode(&b)
		done <- b.Requests
	}()
	time.Sleep(100 * time.Millisecond)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = http.Post(srv.URL+"/hooks/s2", "text/plain", strings.NewReader("x")) }()
	}
	if recs := <-done; len(recs) == 0 {
		t.Fatal("long polling が新しい記録を返さなかった")
	}
	wg.Wait()
	resp, _ := http.Get(srv.URL + "/_control/sinks/s2/stats")
	var st map[string]int
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if st["count"] != 3 || st["max_in_flight"] != 3 {
		t.Fatalf("stats = %v", st)
	}
}

func TestHangTimesOutSender(t *testing.T) {
	srv := httptest.NewServer(NewServer().Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/_control/sinks/s3/behavior", strings.NewReader(`{"rules":[{"hang":true,"times":1}]}`))
	_, _ = http.DefaultClient.Do(req)
	c := &http.Client{Timeout: 300 * time.Millisecond}
	if _, err := c.Post(srv.URL+"/hooks/s3", "text/plain", nil); err == nil {
		t.Fatal("タイムアウトしなかった")
	}
	resp, err := c.Post(srv.URL+"/hooks/s3", "text/plain", nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("規則を使い切った後 = %v %v", resp, err)
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
