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
// pim.enabled (server), a helper client, users.id == pim.owner_user_id and
// the user's own pim.enabled setting (default false, #71). Tools
// switched off by the kill switch are dropped. Fail-closed.
func (s *Server) pimToolsFor(ctx context.Context, login string) []string {
	if !s.Cfg.Pim.Enabled || s.Pim == nil || s.Cfg.Pim.OwnerUserID <= 0 {
		return nil
	}
	if s.pimUserID(ctx, login) != s.Cfg.Pim.OwnerUserID {
		return nil
	}
	if u, err := s.userSettingsFor(ctx, login); err != nil || !u.Pim.Enabled {
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
func (s *Server) runPimTool(r *http.Request, exposed []string, name, args string) string {
	ok := false
	for _, n := range exposed {
		ok = ok || n == name
	}
	if !ok {
		s.Log.Info("pim_tool", "request_id", rid(r), "tool", name, "status", "denied", "error_code", pim.CodeKillSwitch)
		return pim.Denied()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	out, o := pim.Exec(ctx, s.Pim, name, args, time.Local)
	s.Log.Info("pim_tool", "request_id", rid(r), "tool", name, "status", o.Status, "error_code", o.ErrorCode, "count", o.Count, "truncated", o.Truncated)
	return out
}
