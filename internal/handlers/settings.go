package handlers

import (
	"encoding/json"
	"github.com/gun025bba/eve-serving/internal/auth"
	"net/http"
	"strconv"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/store"
)

// GET/PATCH /v1/settings (ui-api-contract §2, §3.9.7). Backed by
// users.settings JSONB (namespaced keys). Only memory.enabled is writable
// today; system.* is read-only server state.

type userSettings struct {
	Memory struct {
		Enabled bool `json:"enabled"`
	} `json:"memory"`
}

func (s *Server) loadUserSettings(r *http.Request) (userSettings, error) {
	var u userSettings
	st, ok := s.Store.(store.UserSettingsStore)
	if !ok {
		return u, nil
	}
	raw, err := st.UserSettings(r.Context(), user(r))
	if err != nil {
		return u, err
	}
	json.Unmarshal(raw, &u) // missing or malformed keys read as defaults (off)
	return u, nil
}

// memoryFor is the effective switch for this request:
// system.memory_available AND users.settings memory.enabled. Fail-closed.
func (s *Server) memoryFor(r *http.Request) bool {
	if !s.memoryOn() {
		return false
	}
	u, err := s.loadUserSettings(r)
	return err == nil && u.Memory.Enabled
}

func (s *Server) settingsView(u userSettings) map[string]any {
	out := map[string]any{"system": map[string]any{"memory_available": s.memoryOn()}}
	if s.memoryOn() {
		out["memory"] = map[string]any{"enabled": u.Memory.Enabled}
	}
	return out
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	u, err := s.loadUserSettings(r)
	if err != nil {
		s.memErr(w, r, err)
		return
	}
	writeNoStore(w, 200, s.settingsView(u))
}

func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	// Settings changes are browser-session only (security Low #40): an API
	// key (Bearer) cannot turn memory on or off. CSRF is checked by authed.
	if id, _ := auth.FromContext(r.Context()); id.Provider != auth.ProviderTailscale {
		s.writeCode(w, r, "scope_denied")
		return
	}
	var b struct {
		Memory *struct {
			Enabled *bool `json:"enabled"`
		} `json:"memory"`
	}
	// strict: unknown keys (incl. system.*), null and type errors → 400
	if !decodeStrict(w, r, &b) || b.Memory == nil || b.Memory.Enabled == nil || !s.memoryOn() {
		s.writeCode(w, r, "invalid")
		return
	}
	st, ok := s.Store.(store.UserSettingsStore)
	if !ok {
		s.writeCode(w, r, "invalid")
		return
	}
	before, err := s.loadUserSettings(r)
	if err != nil {
		s.memErr(w, r, err)
		return
	}
	patch, _ := json.Marshal(map[string]bool{"enabled": *b.Memory.Enabled})
	raw, err := st.MergeUserSettings(r.Context(), user(r), "memory", patch)
	if err != nil {
		s.memErr(w, r, err)
		return
	}
	var after userSettings
	json.Unmarshal(raw, &after)
	s.appendAudit(r.Context(), user(r), audit.Event{At: time.Now().UTC(), Actor: audit.ActorUser, Event: "settings_changed",
		Scope: "security", Status: "ok", Detail: map[string]string{"key": "memory.enabled",
			"before": strconv.FormatBool(before.Memory.Enabled), "after": strconv.FormatBool(after.Memory.Enabled)}})
	writeNoStore(w, 200, s.settingsView(after))
}
