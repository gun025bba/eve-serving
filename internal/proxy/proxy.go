// Package proxy talks to the upstream OpenAI-compatible model server with
// connect timeouts, connection-only retries and an idle (between-bytes) read timeout.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gun025bba/eve-serving/internal/apierr"
)

type Options struct {
	BaseURL        string
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration // max gap between bytes (incl. time to headers)
	Retries        int
	Backoff        time.Duration
	OnRetry        func(attempt int, err error)
}

type Client struct {
	opt  Options
	http *http.Client
}

type dialError struct{ err error }

func (d *dialError) Error() string { return "dial: " + d.err.Error() }
func (d *dialError) Unwrap() error { return d.err }

func New(o Options) *Client {
	d := &net.Dialer{Timeout: o.ConnectTimeout, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, &dialError{err}
			}
			return c, nil
		},
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: o.ReadTimeout,
		DisableCompression:    true,
	}
	return &Client{opt: o, http: &http.Client{Transport: tr}}
}

// IsDialError reports whether err happened while connecting (request never reached upstream).
func IsDialError(err error) bool {
	var d *dialError
	return errors.As(err, &d)
}

func Unavailable() *apierr.Error {
	return apierr.New(503, "upstream_unavailable", "Model server unreachable (is com.llmserving.mlx running?)", "server_error")
}

func (c *Client) Timeout() *apierr.Error {
	return apierr.New(504, "upstream_timeout", fmt.Sprintf("Model server did not respond within %gs", c.opt.ReadTimeout.Seconds()), "server_error")
}

// Do sends a request, retrying only dial failures. The returned body enforces the idle read timeout;
// a stalled read fails with an error for which IsIdleTimeout is true.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, timeout time.Duration) (*http.Response, error) {
	url := strings.TrimRight(c.opt.BaseURL, "/") + path
	for i := 0; ; i++ {
		rctx, cancel := context.WithCancel(ctx)
		var tcancel context.CancelFunc = func() {}
		if timeout > 0 {
			rctx, tcancel = context.WithTimeout(rctx, timeout)
		}
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(rctx, method, url, rd)
		if err != nil {
			cancel()
			tcancel()
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err == nil {
			resp.Body = newIdleBody(resp.Body, c.opt.ReadTimeout, func() { tcancel(); cancel() }, cancel)
			return resp, nil
		}
		tcancel()
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if IsDialError(err) {
			if c.opt.OnRetry != nil {
				c.opt.OnRetry(i+1, err)
			}
			if i >= c.opt.Retries {
				return nil, Unavailable()
			}
			select {
			case <-time.After(c.opt.Backoff * time.Duration(1<<i)):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
			return nil, c.Timeout()
		}
		return nil, apierr.New(502, "upstream_error", "Upstream request failed: "+err.Error(), "server_error")
	}
}

// ErrIdleTimeout is returned by Read when no bytes arrived within the read timeout.
var ErrIdleTimeout = errors.New("upstream idle timeout")

type idleBody struct {
	rc      io.ReadCloser
	d       time.Duration
	timer   *time.Timer
	fired   chan struct{}
	once    sync.Once
	closeFn func()
}

func newIdleBody(rc io.ReadCloser, d time.Duration, closeFn, cancel func()) io.ReadCloser {
	b := &idleBody{rc: rc, d: d, fired: make(chan struct{}), closeFn: closeFn}
	if d > 0 {
		b.timer = time.AfterFunc(d, func() { close(b.fired); cancel() })
	}
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if b.timer != nil {
		select {
		case <-b.fired:
			return n, ErrIdleTimeout
		default:
			if n > 0 {
				b.timer.Reset(b.d)
			}
		}
	}
	return n, err
}

func (b *idleBody) Close() error {
	err := b.rc.Close()
	b.once.Do(func() {
		if b.timer != nil {
			b.timer.Stop()
		}
		b.closeFn()
	})
	return err
}
