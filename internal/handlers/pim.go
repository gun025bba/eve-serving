package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gun025bba/eve-serving/internal/pim"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/tools"
)

// Jarvis M1 read tools (#69). Exposure depends only on the principal's
// users.id (resolved from the auth context) and server settings — never on
// how the request was authenticated.

// pimUserID resolves the request principal to users.id (0 = unknown).
func (s *Server) pimUserID(ctx context.Context, login string) int64 {
	res, ok := s.Store.(store.UserIDResolver)
	if !ok || login == "" {
		return 0
	}
	id, err := res.UserID(ctx, login)
	if err != nil {
		return 0
	}
	return id
}

// pimToolsFor lists the pim tools offered to this principal: none unless
// pim.enabled, a helper client, and users.id == pim.owner_user_id. Tools
// switched off by the kill switch are dropped. Fail-closed.
func (s *Server) pimToolsFor(ctx context.Context, login string) []string {
	if !s.Cfg.Pim.Enabled || s.Pim == nil || s.Cfg.Pim.OwnerUserID <= 0 {
		return nil
	}
	if s.pimUserID(ctx, login) != s.Cfg.Pim.OwnerUserID {
		return nil
	}
	var out []string
	for _, n := range pim.Names() {
		if !tools.Default().Kill.Disabled(n) {
			out = append(out, n)
		}
	}
	return out
}

func withPimTools(defs []any, names []string) []any {
	if len(names) == 0 {
		return defs
	}
	return append(append([]any{}, defs...), pim.ToolDefs(names)...)
}

// runPimTool executes one pim tool call. A call for a tool that is not
// exposed to this principal is denied without touching the helper. Logs
// carry tool, status, error code and count only.
func (s *Server) runPimTool(r *http.Request, exposed []string, name, args string) (string, pim.Outcome) {
	ok := false
	for _, n := range exposed {
		ok = ok || n == name
	}
	if !ok {
		s.Log.Info("pim_tool", "request_id", rid(r), "tool", name, "status", "denied", "error_code", pim.CodeKillSwitch)
		return pim.Denied(), pim.DeniedOutcome(name)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	out, o := pim.Exec(ctx, s.Pim, name, args, time.Local)
	s.Log.Info("pim_tool", "request_id", rid(r), "tool", name, "status", o.Status, "error_code", o.ErrorCode, "count", o.Count, "truncated", o.Truncated)
	return out, o
}

// pimToolEvents builds the two SSE events of a pim tool call (design §11.1),
// the existing chat.completion.tool_call / tool_result shapes: the model's
// arguments as-is for reads ({} for writes), then status, count/truncated and
// the card on success or error_code and an error card on failure. No sources,
// and the card is never stored.
func pimToolEvents(id, name, rawArgs string, round int, o pim.Outcome) (call, result map[string]any) {
	call = map[string]any{"object": "chat.completion.tool_call", "id": id, "tool": name, "args": pim.PublicArgs(name, rawArgs), "round": round}
	result = map[string]any{"object": "chat.completion.tool_result", "id": id, "tool": name}
	for k, v := range o.ResultFields() {
		result[k] = v
	}
	return call, result
}
