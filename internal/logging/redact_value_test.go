package logging

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type strg struct{}

func (strg) String() string { return "SECRET-S" }

func TestRedactor_AllValueKindsAndKeys(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, DEBUG)
	l.SetRedactor(func(s string) string { return strings.ReplaceAll(s, "SECRET", "X") })
	l.Info("m", "b", []byte("SECRET-B"), "e", errors.New("SECRET-E"), "s", strg{}, "m", map[string]any{"k": "SECRET-M"}, "SECRET-KEY", 1)
	if out := buf.String(); strings.Contains(out, "SECRET") {
		t.Fatal(out)
	}
}

func TestWriter_GoesThroughRedactor(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, DEBUG)
	l.SetRedactor(func(s string) string { return strings.ReplaceAll(s, "SECRET", "X") })
	l.Writer(WARN, "http_server_error").Write([]byte("tls: SECRET\n"))
	if out := buf.String(); strings.Contains(out, "SECRET") || !strings.Contains(out, "http_server_error") {
		t.Fatal(out)
	}
}
