// Package apierr defines the gateway's OpenAI-style error envelope.
package apierr

import "fmt"

// Error is a gateway error with HTTP status and stable code.
type Error struct {
	Status  int
	Code    string
	Message string
	Type    string
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

func New(status int, code, msg, etype string) *Error {
	return &Error{Status: status, Code: code, Message: msg, Type: etype}
}

// Body returns {"error":{message,type,code,request_id}}.
func Body(code, msg, etype, rid string) map[string]any {
	return map[string]any{"error": map[string]any{"message": msg, "type": etype, "code": code, "request_id": rid}}
}

func BadRequest(msg string) *Error { return New(400, "bad_request", msg, "invalid_request_error") }
func InvalidKey() *Error {
	return New(401, "invalid_api_key", "Missing or invalid API key. Use 'Authorization: Bearer <key>'.", "authentication_error")
}
func Unauthenticated() *Error {
	return New(401, "unauthenticated", "Authentication required: open via the tailnet HTTPS URL, or send 'Authorization: Bearer <key>'.", "authentication_error")
}
func ForbiddenIdentity() *Error {
	return New(403, "identity_not_allowed", "This identity is not in the gateway allowlist.", "permission_error")
}
func Internal() *Error { return New(500, "internal_error", "Internal gateway error", "server_error") }
