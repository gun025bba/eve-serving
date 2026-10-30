//go:build qa_testhook

package tools_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"github.com/gun025bba/eve-serving/internal/webfetch"
)

var qaDials atomic.Int64

// "dial 0" (§6.8): every dial that survives the guard is counted and refused; the corpus must produce none.
func qaInstallDialCounter(o *webfetch.Options) {
	o.SetDialIPForTest(func(context.Context, string, string) (net.Conn, error) {
		qaDials.Add(1)
		return nil, errors.New("QA: dial intercepted")
	})
}

func qaAssertNoDials(t *testing.T) {
	if n := qaDials.Load(); n != 0 {
		t.Errorf("dial count = %d, want 0", n)
	}
}
