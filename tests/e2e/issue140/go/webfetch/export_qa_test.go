//go:build qaharness && !qa_testhook

package webfetch

import (
	"context"
	"net"
)

// FALLBACK for the exported test hook (see ../testhook/webfetch_testhook.go.txt). Lives in the package's own
// test build, so it only works when this directory's files are copied into internal/webfetch/.
func (o *Options) SetDialIPForTest(fn func(ctx context.Context, network, ipport string) (net.Conn, error)) {
	o.dialIP = fn
}
