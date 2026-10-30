package handlers

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/briefing"
	"net/http"
	"strconv"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/store"
)

// GET/PATCH /v1/settings (ui-api-contract §2, §3.9.7). Backed by
// users.settings JSONB (namespaced keys). Writable: memory.enabled,
// briefing.* (when GW_BRIEFING_ENABLED) and pim.enabled (when
// GW_PIM_ENABLED). system.* is read-only server state.

type userSettings struct {
	Memory struct {
		// Enabled is a pointer so a missing key reads as the 5.0.0 default
		// (on, opt-out); an explicit false stays off.
		Enabled *bool `json:"enabled"`
	} `json:"memory"`
	Briefing briefing.Settings `json:"briefing"`
	Pim      struct {
		Enabled bool `json:"enabled"`
	} `json:"pim"`
	// Proactive is read-only here: there is no writer yet, so it stays false
	// and scheduled briefings stay off (gate, #71).
	Proactive struct {
		Enabled bool `json:"enabled"`
	} `json:"proactive"`
	UI struct {
		MemoryIntroSeen bool `json:"memory_intro_seen"`
	} `json:"ui"`
}

// memoryEnabled is the per-user switch: missing key = true (5.0.0, opt-out).
func (u userSettings) memoryEnabled() bool { return u.Memory.Enabled == nil || *u.Memory.Enabled }

func (s *Server) loadUserSettings(r *http.Request) (userSettings, error) {
	return s.userSettingsFor(r.Context(), user(r))
}

func (s *Server) userSettingsFor(ctx context.Context, login string) (userSettings, error) {
	var u userSettings
	u.Briefing = briefing.Defaults()
	st, ok := s.Store.(store.UserSettingsStore)
	if !ok {
		return u, nil
	}
	raw, err := st.UserSettings(ctx, login)
	if err != nil {
		return u, err
	}
	if json.Unmarshal(raw, &u) != nil { // malformed keys read as defaults (off)
		u = userSettings{}
		u.Briefing = briefing.Defaults()
	}
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

// settingsView renders the settings. memory.enabled is the effective value for
// this principal: without the interactive scope memory is fixed off (see
// memoryFor), so an API key never reads "true" (security Low, 5.0.0 review).
func (s *Server) settingsView(r *http.Request, u userSettings) map[string]any {
	out := map[string]any{"system": map[string]any{"memory_available": s.memoryOn()},
		"ui": map[string]any{"memory_intro_seen": u.UI.MemoryIntroSeen}}
	if s.memoryOn() {
		out["memory"] = map[string]any{"enabled": u.memoryEnabled() && hasScope(r, auth.ScopeInteractive)}
	}
	if s.Cfg.Briefing.Enabled {
		out["briefing"] = u.Briefing
	}
	if s.Cfg.Pim.Enabled {
		out["pim"] = map[string]any{"enabled": u.Pim.Enabled}
	}
	return out
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	u, err := s.loadUserSettings(r)
	if err != nil {
		s.memErr(w, r, err)
		return
	}
	writeNoStore(w, 200, s.settingsView(r, u))
}

type briefingPatch struct {
	Enabled       *bool   `json:"enabled"`
	Time          *string `json:"time"`
	TZ            *string `json:"tz"`
	SkipIfEmpty   *bool   `json:"skip_if_empty"`
	Delivery      *string `json:"delivery"`
	RetentionDays *int    `json:"retention_days"`
}

func (p *briefingPatch) apply(b briefing.Settings) (briefing.Settings, map[string][2]string, bool) {
	ch := map[string][2]string{}
	setB := func(k string, dst *bool, v *bool) {
		if v != nil {
			ch[k] = [2]string{strconv.FormatBool(*dst), strconv.FormatBool(*v)}
			*dst = *v
		}
	}
	setS := func(k string, dst *string, v *string) {
		if v != nil {
			ch[k] = [2]string{*dst, *v}
			*dst = *v
		}
	}
	setB("briefing.enabled", &b.Enabled, p.Enabled)
	setS("briefing.time", &b.Time, p.Time)
	setS("briefing.tz", &b.TZ, p.TZ)
	setB("briefing.skip_if_empty", &b.SkipIfEmpty, p.SkipIfEmpty)
	setS("briefing.delivery", &b.Delivery, p.Delivery)
	if p.RetentionDays != nil {
		ch["briefing.retention_days"] = [2]string{strconv.Itoa(b.RetentionDays), strconv.Itoa(*p.RetentionDays)}
		b.RetentionDays = *p.RetentionDays
	}
	return b, ch, len(ch) > 0
}

func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	// Settings changes need the interactive scope (security Low #40): an API
	// key cannot turn memory, briefing or pim on or off. CSRF is checked by
	// authed.
	if !s.requireScope(w, r, auth.ScopeInteractive) {
		return
	}
	var b struct {
		Memory *struct {
			Enabled *bool `json:"enabled"`
		} `json:"memory"`
		Briefing *briefingPatch `json:"briefing"`
		Pim      *struct {
			Enabled *bool `json:"enabled"`
		} `json:"pim"`
		UI *struct {
			MemoryIntroSeen *bool `json:"memory_intro_seen"`
		} `json:"ui"`
	}
	// strict: unknown keys (incl. system.*, proactive.*), null and type
	// errors → 400. A namespace whose server switch is off is unknown.
	// Allowlist: memory.enabled (needs memory_available), briefing.*, pim.enabled,
	// ui.memory_intro_seen.
	if !decodeStrict(w, r, &b) || (b.Memory == nil && b.Briefing == nil && b.Pim == nil && b.UI == nil) ||
		(b.Memory != nil && (b.Memory.Enabled == nil || !s.memoryOn())) ||
		(b.Briefing != nil && !s.Cfg.Briefing.Enabled) ||
		(b.Pim != nil && (b.Pim.Enabled == nil || !s.Cfg.Pim.Enabled)) ||
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
	var (
		nb      briefing.Settings
		changes map[string][2]string
	)
	if b.Briefing != nil {
		var changed bool
		nb, changes, changed = b.Briefing.apply(before.Briefing)
		if !changed || nb.Validate() != nil {
			s.writeCode(w, r, "invalid")
			return
		}
	}
	type write struct {
		ns    string
		patch any
	}
	var writes []write
	if b.Memory != nil {
		writes = append(writes, write{"memory", map[string]bool{"enabled": *b.Memory.Enabled}})
	}
	if b.Briefing != nil {
		writes = append(writes, write{"briefing", nb})
	}
	if b.Pim != nil {
		writes = append(writes, write{"pim", map[string]bool{"enabled": *b.Pim.Enabled}})
	}
	if b.UI != nil {
		writes = append(writes, write{"ui", map[string]bool{"memory_intro_seen": *b.UI.MemoryIntroSeen}})
	}
	var raw json.RawMessage
	for _, wr := range writes {
		patch, _ := json.Marshal(wr.patch)
		if raw, err = st.MergeUserSettings(r.Context(), user(r), wr.ns, patch); err != nil {
			s.memErr(w, r, err)
			return
		}
	}
	var after userSettings
	after.Briefing = briefing.Defaults()
	json.Unmarshal(raw, &after)
	now := time.Now().UTC()
	if b.Memory != nil {
		changes = withChange(changes, "memory.enabled", before.memoryEnabled(), after.memoryEnabled())
	}
	if b.Pim != nil {
		changes = withChange(changes, "pim.enabled", before.Pim.Enabled, after.Pim.Enabled)
	}
	for _, k := range sortedKeys(changes) {
		s.appendAudit(r.Context(), user(r), audit.Event{At: now, Actor: audit.ActorUser, Event: "settings_changed",
			Scope: "security", Status: "ok", Detail: map[string]string{"key": k, "before": changes[k][0], "after": changes[k][1]}})
	}
	writeNoStore(w, 200, s.settingsView(r, after))
}

func withChange(m map[string][2]string, k string, before, after bool) map[string][2]string {
	if m == nil {
		m = map[string][2]string{}
	}
	m[k] = [2]string{strconv.FormatBool(before), strconv.FormatBool(after)}
	return m
}

func sortedKeys(m map[string][2]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
