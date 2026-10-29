// Test hook for the SSRF QA harness (issue #140, architect decision 2).
// Compiled only with -tags qa_testhook, so production binaries cannot reach it. Production still always
// dials through net.Dialer with Guard.Control; this only replaces the raw TCP dial of an ALREADY VALIDATED ip:port.
//
//go:build qa_testhook

package webfetch

import (
	"context"
	"net"
)

// SetDialIPForTest replaces the dial of a validated ip:port (e.g. to reach an httptest server or to count dials).
func (o *Options) SetDialIPForTest(fn func(ctx context.Context, network, ipport string) (net.Conn, error)) {
	o.dialIP = fn
}
