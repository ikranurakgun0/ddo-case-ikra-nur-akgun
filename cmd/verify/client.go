package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

type failure struct{ msg string }

type client struct {
	base string
	http *http.Client
}

type resp struct {
	code int
	body map[string]any
	hdr  http.Header
}

func (c *client) do(method, path, token string, body any, hdr map[string]string) resp {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		panic(failure{err.Error()})
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := c.http.Do(req)
	if err != nil {
		return resp{code: 0, body: map[string]any{"transport_error": err.Error()}}
	}
	defer r.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(r.Body).Decode(&m)
	return resp{code: r.StatusCode, body: m, hdr: r.Header}
}

// expect koşul sağlanmazsa kontrolü FAIL ile sonlandırır.
func expect(cond bool, format string, a ...any) {
	if !cond {
		panic(failure{fmt.Sprintf(format, a...)})
	}
}

func (r resp) must(want int, what string) resp {
	expect(r.code == want, "%s: %d bekleniyordu, %d geldi (%v)", what, want, r.code, r.body)
	return r
}

func (r resp) errCode() string {
	e, _ := r.body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func (r resp) str(k string) string { s, _ := r.body[k].(string); return s }
func (r resp) num(k string) int64  { f, _ := r.body[k].(float64); return int64(f) }
func (r resp) arr(k string) []any  { a, _ := r.body[k].([]any); return a }
func (r resp) obj(k string) map[string]any {
	m, _ := r.body[k].(map[string]any)
	return m
}

func num(m map[string]any, k string) int64 { f, _ := m[k].(float64); return int64(f) }

// parallel n işi eşzamanlı çalıştırır ve sonuçları sırayla döner.
func parallel(n int, fn func(i int) resp) []resp {
	out := make([]resp, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out[i] = fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return out
}

func sortedInts(in []int64) []int64 {
	out := append([]int64(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func today() time.Time { return time.Now().UTC().Truncate(24 * time.Hour) }

func day(offset int) string { return today().AddDate(0, 0, offset).Format("2006-01-02") }
