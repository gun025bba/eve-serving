package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/tools"
)

// Audit failures are logged and counted but do not block the call in M2
// (fail-closed auditing for write tools arrives with M3, §15.2). They feed
// the audit circuit breaker, which disables the web tools after 3 failures
// in 5 minutes.
func (s *Server) appendAudit(ctx context.Context, login string, e audit.Event) {
	if s.Audit == nil {
		return
	}
	// detached from the request so a client disconnect does not drop the row
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := s.Audit.Append(actx, login, e); err != nil {
		s.M.AuditWriteFail.Inc()
		s.AuditBreaker.Fail()
		s.Log.Error("audit_write_failed", "event", e.Event, "err", err.Error())
	}
}

func (s *Server) toolEvent(pr *chatPersist, c tools.Call, ev string) audit.Event {
	e := audit.Event{At: time.Now(), Actor: audit.ActorUser, Event: ev, Tool: c.Tool, ToolCallID: c.ID,
		ToolVersion: toolVersion(c.Tool), PayloadSHA256: c.ArgHash}
	if pr != nil {
		e.ThreadID, e.MessageID = pr.threadID, pr.userMsgID
	}
	return e
}

func toolVersion(name string) string {
	if m, ok := tools.Default().Reg.Get(name); ok {
		return m.Version
	}
	return ""
}

func (s *Server) auditCall(r *http.Request, pr *chatPersist, c tools.Call, ev string) {
	e := s.toolEvent(pr, c, ev)
	if ev == audit.EvCallDone {
		e.Status = c.Status
		if c.BlockReason != "" || c.Blocked > 0 {
			e.Detail = map[string]string{}
			if c.BlockReason != "" {
				e.Detail["block_reason"] = c.BlockReason
			}
			if c.Blocked > 0 {
				e.Detail["blocked_count"] = strconv.Itoa(c.Blocked)
			}
		}
	}
	s.appendAudit(r.Context(), user(r), e)
}

// auditDomainBlock records one blocked host: hashed host, rule id and hop
// only. The host itself never reaches the model, logs or metrics labels.
func (s *Server) auditDomainBlock(r *http.Request, pr *chatPersist, c tools.Call, reason, rule, host string, hop int) {
	if c.Tool == "web_search" { // per dropped result; blocked fetch calls are counted in observeCall
		s.M.ToolDenied.WithLabelValues(c.Tool, reason).Inc()
	}
	e := s.toolEvent(pr, c, audit.EvDomainBlocked)
	e.Status = "blocked"
	e.Detail = map[string]string{"host_hash": audit.HashHost(host), "reason": reason, "hop": strconv.Itoa(hop)}
	if rule != "" {
		e.Detail["rule"] = rule
	}
	s.appendAudit(r.Context(), user(r), e)
}

// MemoryLedgerBlocksWeb reports whether the web tools must be disabled
// because auditing would only be in memory (no database) outside GW_ENV=dev.
func MemoryLedgerBlocksWeb(c config.Config, memoryLedger bool) bool {
	return memoryLedger && !strings.EqualFold(c.Env, "dev")
}
