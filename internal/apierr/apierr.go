// Package apierr defines the gateway's OpenAI-style error envelope.
package apierr

import "fmt"

// Error is a gateway error with HTTP status and stable code.
type Error struct {
	Status  int
	Code    string
	Message string
	Type    string
	// Extra holds additional fields merged into the error object (e.g.
	// message_id on 409 generation_in_progress, #30). Never secrets.
	Extra map[string]any
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
	return New(401, "unauthenticated", "Authentication required: pair this browser (eve-gw pair), or send 'Authorization: Bearer <key>'.", "authentication_error")
}
func InvalidSession() *Error {
	return New(401, "invalid_session", "Session expired or revoked: pair this browser again.", "authentication_error")
}
func ScopeDenied() *Error {
	return New(403, "scope_denied", "This route needs a paired browser session; API keys are not accepted.", "permission_error")
}
func ForbiddenIdentity() *Error {
	return New(403, "identity_not_allowed", "This identity is not in the gateway allowlist.", "permission_error")
}
func Internal() *Error { return New(500, "internal_error", "Internal gateway error", "server_error") }
