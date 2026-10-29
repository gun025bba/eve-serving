//go:build qaharness && !qa_testhook

package tools_test

import (
	"testing"

	"github.com/gun025bba/eve-serving/internal/webfetch"
)

// Without the exported test hook the dial count cannot be observed; the stub resolver never returns a public
// IP for non-stub names, so no connection can be opened anyway.
func qaInstallDialCounter(*webfetch.Options) {}
func qaAssertNoDials(t *testing.T) {
	t.Log("dial counter: hook not compiled (-tags qa_testhook); resolver stub guarantees no public dial")
}
