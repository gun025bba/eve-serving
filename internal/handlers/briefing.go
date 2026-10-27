package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/briefing"
	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/pim"
)

// Morning briefing (#71). Only the preview endpoint is wired here: it builds
// the briefing for the caller and returns it, without saving or sending
// anything. There is no proactive scheduler in the gateway yet, so nothing
// runs briefing.Run on a schedule; when one lands it uses briefingDeps with
// Save set to the in-app proactive message store.

var errBriefingTruncated = errors.New("briefing model output hit max_tokens")

// briefingDeps builds the per-user dependencies. Data access is decided by
// users.id and settings only (pimToolsFor), never by the auth method.
func (s *Server) briefingDeps(r *http.Request, u userSettings) briefing.Deps {
	d := briefing.Deps{Now: time.Now}
	if len(s.pimToolsFor(r.Context(), user(r))) == len(pim.Names()) {
		// both read tools on (owner, pim.enabled, kill switch): use the helper
		d.Pim = s.Pim
	}
	d.Memory = func(ctx context.Context) string {
		// approved items only, and only after the user has seen the memory
		// intro; any failure (503, load error) injects nothing.
		if !s.memoryFor(r) || !u.UI.MemoryIntroSeen {
			return ""
		}
		doc, err := s.Memory.Load(s.memUID(r))
		if err != nil {
			return ""
		}
		limit := s.Cfg.Memory.InjectCapTokens
		if limit == 0 {
			limit = memory.DefaultCapTokens
		}
		blk, err := memory.Block(doc.Items, time.Now(), limit)
		if err != nil {
			return ""
		}
		return blk
	}
	d.Generate = func(ctx context.Context, msgs []map[string]any) (string, error) {
		ms := make([]any, len(msgs))
		for i, m := range msgs {
			ms[i] = m
		}
		// no "tools" key: the briefing model call cannot call anything.
		// max_tokens/temperature bound the output; the user message ends
		// with /no_think (briefing.messages).
		v, _, err := s.upstreamJSON(ctx, map[string]any{"model": s.Cfg.Model, "messages": ms,
			"max_tokens": briefing.MaxTokens, "temperature": briefing.Temperature})
		if err != nil {
			return "", err
		}
		// a cut-off answer would be stored as a partial briefing: fail it
		if cs, _ := v["choices"].([]any); len(cs) > 0 {
			if c, _ := cs[0].(map[string]any); c != nil {
				if fr, _ := c["finish_reason"].(string); fr == "length" {
					return "", errBriefingTruncated
				}
			}
		}
		return strings.TrimSpace(firstMessage(v).Content), nil
	}
	return d
}

// POST /v1/briefing/preview — interactive scope; 404 when GW_BRIEFING_ENABLED
// is off. Returns the text and content-free section status.
func (s *Server) previewBriefing(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, auth.ScopeInteractive) {
		return
	}
	if !s.Cfg.Briefing.Enabled {
		s.writeCode(w, r, "not_found")
		return
	}
	u, err := s.loadUserSettings(r)
	if err != nil {
		s.memErr(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res := briefing.Preview(ctx, s.briefingDeps(r.WithContext(ctx), u), u.Briefing)
	s.Log.Info("briefing_preview", "request_id", rid(r), "status", res.Status, "sections", res.Sections, "fixed", res.Fixed, "fallback", res.Fallback)
	if res.Status != briefing.StatusOK {
		writeNoStore(w, 502, map[string]any{"status": res.Status, "sections": res.Sections})
		return
	}
	writeNoStore(w, 200, map[string]any{"status": res.Status, "text": res.Text, "sections": res.Sections})
}
