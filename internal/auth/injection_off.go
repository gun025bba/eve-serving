//go:build !e2e

package auth

import "errors"

// CheckInjection: identity injection is not compiled into this binary.
func CheckInjection(InjectionCheck) error {
	return errors.New("GW_TEST_IDENTITY_INJECTION is set but this binary was built without -tags e2e; refusing to start")
}
