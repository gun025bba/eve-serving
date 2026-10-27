package handlers

import (
	"testing"

	"github.com/gun025bba/eve-serving/internal/memory"
)

func TestRejectCode_FilterLimitOwnCode(t *testing.T) {
	cases := map[string]string{"filter_limit": "filter_limit", "sensitive_politics": "sensitive_category", "secret_aws": "secret_detected"}
	for reason, want := range cases {
		if got := rejectCode(&memory.RejectError{Reason: reason}); got != want {
			t.Errorf("%s -> %s, want %s", reason, got, want)
		}
	}
	if errorStatus["filter_limit"] != 422 {
		t.Error("filter_limit must map to 422")
	}
}
