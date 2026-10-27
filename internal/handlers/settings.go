package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/store"
)

// GET/PATCH /v1/settings (ui-api-contract §2, §3.9.7). Backed by
// users.settings JSONB (namespaced keys). Only memory.enabled is writable
// today; system.* is read-only server state.

type userSettings struct {
	Memory struct {
		// Enabled is a pointer so a missing key reads as the 5.0.0 default
		// (on, opt-out); an explicit false stays off.
		Enabled *bool `json:"enabled"`
	} `json:"memory"`
	UI struct {
		MemoryIntroSeen bool `json:"memory_intro_seen"`
	} `json:"ui"`
}

// memoryEnabled is the per-user switch: missing key = true (5.0.0, opt-out).
func (u userSettings) memoryEnabled() bool { return u.Memory.Enabled == nil || *u.Memory.Enabled }

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
// A principal without the interactive scope has memory fixed off (5.0.0).
func (s *Server) memoryFor(r *http.Request) bool {
	if !s.memoryOn() || !hasScope(r, auth.ScopeInteractive) {
		return false
	}
	u, err := s.loadUserSettings(r)
	return err == nil && u.memoryEnabled()
}

// memoryActive additionally requires the first-use intro to have been seen
// (ui.memory_intro_seen): until then nothing is injected into prompts and
// the propose tool is not offered (5.0.0, held until the user is informed).
func (s *Server) memoryActive(r *http.Request) bool {
	if !s.memoryFor(r) {
		return false
	}
	u, err := s.loadUserSettings(r)
	return err == nil && u.UI.MemoryIntroSeen
}

func (s *Server) settingsView(u userSettings) map[string]any {
	out := map[string]any{"system": map[string]any{"memory_available": s.memoryOn()},
		"ui": map[string]any{"memory_intro_seen": u.UI.MemoryIntroSeen}}
	if s.memoryOn() {
		out["memory"] = map[string]any{"enabled": u.memoryEnabled()}
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
	// Settings changes need the interactive scope (security Low #40): an API
	// key cannot turn memory on or off. CSRF is checked by authed.
	if !s.requireScope(w, r, auth.ScopeInteractive) {
		return
	}
	var b struct {
		Memory *struct {
			Enabled *bool `json:"enabled"`
		} `json:"memory"`
		UI *struct {
			MemoryIntroSeen *bool `json:"memory_intro_seen"`
		} `json:"ui"`
	}
	// strict: unknown keys (incl. system.*), null and type errors → 400.
	// Allowlist: memory.enabled (needs memory_available), ui.memory_intro_seen.
	if !decodeStrict(w, r, &b) || (b.Memory == nil && b.UI == nil) ||
		(b.Memory != nil && (b.Memory.Enabled == nil || !s.memoryOn())) ||
		(b.UI != nil && b.UI.MemoryIntroSeen == nil) {
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
	var raw []byte
	if b.Memory != nil {
		patch, _ := json.Marshal(map[string]bool{"enabled": *b.Memory.Enabled})
		if raw, err = st.MergeUserSettings(r.Context(), user(r), "memory", patch); err != nil {
			s.memErr(w, r, err)
			return
		}
	}
	if b.UI != nil {
		patch, _ := json.Marshal(map[string]bool{"memory_intro_seen": *b.UI.MemoryIntroSeen})
		if raw, err = st.MergeUserSettings(r.Context(), user(r), "ui", patch); err != nil {
			s.memErr(w, r, err)
			return
		}
	}
	var after userSettings
	json.Unmarshal(raw, &after)
	if b.Memory != nil {
		s.appendAudit(r.Context(), user(r), audit.Event{At: time.Now().UTC(), Actor: audit.ActorUser, Event: "settings_changed",
			Scope: "security", Status: "ok", Detail: map[string]string{"key": "memory.enabled",
				"before": strconv.FormatBool(before.memoryEnabled()), "after": strconv.FormatBool(after.memoryEnabled())}})
	}
	writeNoStore(w, 200, s.settingsView(after))
}
