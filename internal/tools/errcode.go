package tools

import (
	"github.com/gun025bba/eve-serving/internal/policy/domains"
	"github.com/gun025bba/eve-serving/internal/webfetch"
)

// Public error codes carried by the SSE tool_result event and the tool
// message. They are the only failure detail clients and the model get: no
// rule ids, hosts, reasons or internal error strings.
const (
	ErrCodeSSRF      = "ssrf"
	ErrCodePerm      = "permission_denied"
	ErrCodeKill      = "kill_switch"
	ErrCodeTimeout   = "timeout"
	ErrCodeInvalid   = "invalid_arguments"
	ErrCodeSearch    = "search_unavailable"
	ErrCodeFetch     = "fetch_failed"
	ErrCodeRate      = "rate_limited"
	ErrCodeQuery     = "query_rejected"
	ErrCodeBudget    = "budget_exhausted"
	ErrCodeDup       = "duplicate_query"
	ErrCodeContent   = "unsupported_content"
	ErrCodeRedirect  = "redirect_blocked"
	ErrCodeUnknown   = "unknown_tool"
	ErrCodeSuspected = "permission_denied"
)

var reasonCodes = map[string]string{
	// every block whose target is a private/internal/invalid address or name
	webfetch.ReasonSSRF: ErrCodeSSRF, webfetch.ReasonHost: ErrCodeSSRF, webfetch.ReasonPort: ErrCodeSSRF,
	webfetch.ReasonScheme: ErrCodeSSRF, webfetch.ReasonURL: ErrCodeSSRF,
	domains.ReasonIPLiteral:     ErrCodeSSRF,
	domains.ReasonIPLiteralPriv: ErrCodeSSRF,
	domains.ReasonInvalidHost:   ErrCodeSSRF,
	// policy
	domains.ReasonDomainDenied:   ErrCodePerm,
	domains.ReasonNotAllowlisted: ErrCodePerm,
	domains.ReasonPolicyUnloaded: ErrCodeKill,
	webfetch.ReasonAllowlist:     ErrCodePerm,
	ReasonToolsDisabled:          ErrCodeKill,
	ReasonDisabled:               ErrCodeKill,
	// budgets
	ReasonLimit:             ErrCodeBudget,
	ReasonRate:              ErrCodeRate,
	ReasonQuery:             ErrCodeQuery,
	webfetch.ReasonRedirect: ErrCodeRedirect, webfetch.ReasonContentType: ErrCodeContent,
}

// PublicCode maps a finished call to its public error code ("" when ok).
func PublicCode(c *Call) string {
	if c.ErrorCode != "" {
		return c.ErrorCode
	}
	switch c.Status {
	case "ok", "":
		return ""
	case "timeout":
		return ErrCodeTimeout
	}
	if c.BlockReason != "" {
		if code, ok := reasonCodes[c.BlockReason]; ok {
			return code
		}
		return ErrCodePerm
	}
	switch {
	case c.Error == "unknown tool":
		return ErrCodeUnknown
	case c.Error == "invalid arguments json":
		return ErrCodeInvalid
	case c.Tool == "web_fetch":
		return ErrCodeFetch
	}
	return ErrCodeSearch
}

// QueryRejectedMessage is what the model is told for every rejected query,
// whichever rule fired (rev 7, §4.3.1 "fixed message"): no rule id, no word,
// no count. Changing it needs a security review (TestEcho_RejectionMessageFixed).
const QueryRejectedMessage = "검색어를 보낼 수 없습니다. 사용자 메시지에 있는 말만으로 다시 쓰세요."

var publicMsg = map[string]string{
	ErrCodeSSRF: "target not allowed", ErrCodePerm: "not permitted", ErrCodeKill: "web tools are disabled",
	ErrCodeTimeout: "timed out", ErrCodeInvalid: "invalid arguments", ErrCodeSearch: "search unavailable",
	ErrCodeFetch: "fetch failed", ErrCodeRate: "rate limited", ErrCodeQuery: QueryRejectedMessage,
	ErrCodeBudget: "budget exhausted", ErrCodeDup: "duplicate request", ErrCodeContent: "unsupported content",
	ErrCodeRedirect: "redirect not allowed", ErrCodeUnknown: "unknown tool",
}
