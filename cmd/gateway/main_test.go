package main

import "testing"

func TestCheckLoopback(t *testing.T) {
	for h, ok := range map[string]bool{"127.0.0.1": true, "::1": true, "[::1]": true, "0.0.0.0": false, "": false, "localhost": false,
		"127.0.0.2": false, "::": false} {
		if err := checkLoopback(h); (err == nil) != ok {
			t.Errorf("%q: %v", h, err)
		}
	}
}
