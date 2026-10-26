package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gun025bba/llm-serving/internal/apierr"
)

func code(err error) string {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestUnavailableAfterRetries(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	n := 0
	c := New(Options{BaseURL: "http://" + addr, ConnectTimeout: time.Second, ReadTimeout: time.Second, Retries: 2, Backoff: time.Millisecond, OnRetry: func(int, error) { n++ }})
	_, err := c.Do(context.Background(), "GET", "/v1/models", nil, 0)
	if code(err) != "upstream_unavailable" || n != 3 {
		t.Fatalf("err=%v attempts=%d", err, n)
	}
}

func TestHeaderTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer s.Close()
	c := New(Options{BaseURL: s.URL, ConnectTimeout: time.Second, ReadTimeout: 50 * time.Millisecond})
	_, err := c.Do(context.Background(), "GET", "/", nil, 0)
	if code(err) != "upstream_timeout" {
		t.Fatal(err)
	}
}

func TestIdleBodyTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data: a\n\n"))
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond)
	}))
	defer s.Close()
	c := New(Options{BaseURL: s.URL, ConnectTimeout: time.Second, ReadTimeout: 100 * time.Millisecond})
	resp, err := c.Do(context.Background(), "GET", "/", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, ErrIdleTimeout) {
		t.Fatalf("want idle timeout, got %v", err)
	}
}
