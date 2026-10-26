package handlers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"

	"github.com/gun025bba/eve-serving/internal/auth"
)

// Most handler tests act as the tailscale-serve UI path. Since 4.0.0 identity
// headers are trusted only on the unix socket listener, so these helpers mark
// connections/requests the way the socket listener's ConnContext does.
// socket_test.go covers the real socket and the TCP path.

func newTrustedServer(h http.Handler) *httptest.Server {
	ts := httptest.NewUnstartedServer(h)
	ts.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context { return auth.WithTrustedSocket(ctx) }
	ts.Start()
	return ts
}

func viaSocket(r *http.Request) *http.Request {
	return r.WithContext(auth.WithTrustedSocket(r.Context()))
}
