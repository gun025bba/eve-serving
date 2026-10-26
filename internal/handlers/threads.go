package handlers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gun025bba/llm-serving/internal/apierr"
	"github.com/gun025bba/llm-serving/internal/auth"
	"github.com/gun025bba/llm-serving/internal/store"
)

const maxImportBody = 5 << 20

func persistenceDisabled() *apierr.Error {
	return apierr.New(503, "persistence_disabled", "Conversation storage is not configured on this gateway (GW_DATABASE_URL unset)", "server_error")
}
func notFound(what string) *apierr.Error {
	return apierr.New(404, "not_found", what+" not found", "invalid_request_error")
}

// storeErr maps store errors to API errors.
func storeErr(err error, what string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return notFound(what)
	case errors.Is(err, store.ErrInvalid):
		return apierr.BadRequest("Invalid " + strings.ToLower(what))
	}
	return err
}

// persisted wraps a handler that needs the store.
func (s *Server) persisted(h http.HandlerFunc) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request) {
		if s.Store == nil {
			s.fail(w, r, persistenceDisabled())
			return
		}
		h(w, r)
	})
}

func user(r *http.Request) string {
	id, _ := auth.FromContext(r.Context())
	return id.User
}

func intParam(r *http.Request, k string, def, lo, hi int) (int, error) {
	v := r.URL.Query().Get(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, apierr.BadRequest("'" + k + "' must be an integer in [" + strconv.Itoa(lo) + "," + strconv.Itoa(hi) + "]")
	}
	return n, nil
}

func decodeJSON(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return apierr.New(413, "payload_too_large", "Request body too large", "invalid_request_error")
		}
		return apierr.BadRequest("Invalid JSON body: " + trunc(err.Error(), 200))
	}
	return nil
}

func encCursor(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
func decCursor(c string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	return string(b), err == nil
}

func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", 20, 1, 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	archived := false
	if v := r.URL.Query().Get("archived"); v != "" {
		if archived, err = strconv.ParseBool(v); err != nil {
			s.fail(w, r, apierr.BadRequest("'archived' must be true or false"))
			return
		}
	}
	var after *store.ThreadCursor
	if c := r.URL.Query().Get("cursor"); c != "" {
		raw, ok := decCursor(c)
		ns, id, ok2 := strings.Cut(raw, "|")
		n, perr := strconv.ParseInt(ns, 10, 64)
		if !ok || !ok2 || perr != nil {
			s.fail(w, r, apierr.BadRequest("Invalid cursor"))
			return
		}
		after = &store.ThreadCursor{UpdatedAt: time.Unix(0, n).UTC(), ID: id}
	}
	ts, err := s.Store.ListThreads(r.Context(), user(r), archived, limit+1, after)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var next any
	if len(ts) > limit {
		ts = ts[:limit]
		l := ts[limit-1]
		next = encCursor(strconv.FormatInt(l.UpdatedAt.UnixNano(), 10) + "|" + l.ID)
	}
	writeJSON(w, 200, map[string]any{"data": ts, "next_cursor": next})
}

func (s *Server) createThread(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title *string `json:"title"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		s.fail(w, r, apierr.New(413, "payload_too_large", "Request body too large", "invalid_request_error"))
		return
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := decodeJSON(bytes.NewReader(raw), &b); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	title := ""
	if b.Title != nil {
		title = store.Preview(*b.Title, 200)
	}
	t, err := s.Store.CreateThread(r.Context(), user(r), title)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, t)
}

func (s *Server) getThread(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetThread(r.Context(), user(r), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, storeErr(err, "Thread"))
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) patchThread(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
	}
	if err := decodeJSON(http.MaxBytesReader(w, r.Body, 64<<10), &b); err != nil {
		s.fail(w, r, err)
		return
	}
	if b.Title != nil {
		t := store.Preview(*b.Title, 200)
		b.Title = &t
	}
	t, err := s.Store.UpdateThread(r.Context(), user(r), r.PathValue("id"), b.Title, b.Archived)
	if err != nil {
		s.fail(w, r, storeErr(err, "Thread"))
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) deleteThread(w http.ResponseWriter, r *http.Request) {
	yes := true
	if _, err := s.Store.UpdateThread(r.Context(), user(r), r.PathValue("id"), nil, &yes); err != nil {
		s.fail(w, r, storeErr(err, "Thread"))
		return
	}
	w.WriteHeader(204)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", 50, 1, 200)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ms, more, err := s.Store.ListMessages(r.Context(), user(r), r.PathValue("id"), limit, r.URL.Query().Get("before"))
	if err != nil {
		s.fail(w, r, storeErr(err, "Thread or message"))
		return
	}
	writeJSON(w, 200, map[string]any{"data": ms, "has_more": more})
}

func (s *Server) putFeedback(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Rating  string  `json:"rating"`
		Comment *string `json:"comment"`
	}
	if err := decodeJSON(http.MaxBytesReader(w, r.Body, 64<<10), &b); err != nil {
		s.fail(w, r, err)
		return
	}
	if b.Rating != "up" && b.Rating != "down" {
		s.fail(w, r, apierr.BadRequest("'rating' must be \"up\" or \"down\""))
		return
	}
	f, err := s.Store.SetFeedback(r.Context(), user(r), r.PathValue("id"), b.Rating, b.Comment)
	if err != nil {
		s.fail(w, r, storeErr(err, "Message"))
		return
	}
	writeJSON(w, 200, f)
}

func (s *Server) deleteFeedback(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteFeedback(r.Context(), user(r), r.PathValue("id")); err != nil {
		s.fail(w, r, storeErr(err, "Message"))
		return
	}
	w.WriteHeader(204)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 {
		s.fail(w, r, apierr.BadRequest("'q' is required (max 200 bytes)"))
		return
	}
	limit, err := intParam(r, "limit", 20, 1, 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	off := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		raw, ok := decCursor(c)
		n, perr := strconv.Atoi(raw)
		if !ok || perr != nil || n < 0 {
			s.fail(w, r, apierr.BadRequest("Invalid cursor"))
			return
		}
		off = n
	}
	hits, err := s.Store.Search(r.Context(), user(r), q, limit+1, off)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var next any
	if len(hits) > limit {
		hits = hits[:limit]
		next = encCursor(strconv.Itoa(off + limit))
	}
	writeJSON(w, 200, map[string]any{"data": hits, "next_cursor": next})
}

func (s *Server) importThreads(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBody))
	if err != nil {
		s.fail(w, r, apierr.New(413, "payload_too_large", "Import body exceeds 5 MB; split it into batches", "invalid_request_error"))
		return
	}
	var b struct {
		Threads []store.ImportThread `json:"threads"`
	}
	if err := decodeJSON(bytes.NewReader(raw), &b); err != nil {
		s.fail(w, r, err)
		return
	}
	if len(b.Threads) == 0 {
		s.fail(w, r, apierr.BadRequest("'threads' must be a non-empty array"))
		return
	}
	for i, t := range b.Threads {
		if t.ClientID == "" || t.CreatedAt.IsZero() {
			s.fail(w, r, apierr.BadRequest("threads["+strconv.Itoa(i)+"]: client_id and created_at are required"))
			return
		}
		for j, m := range t.Messages {
			if m.CreatedAt.IsZero() || (m.Role != "user" && m.Role != "assistant" && m.Role != "system" && m.Role != "tool") {
				s.fail(w, r, apierr.BadRequest("threads["+strconv.Itoa(i)+"].messages["+strconv.Itoa(j)+"]: valid role and created_at are required"))
				return
			}
		}
	}
	type result struct {
		ClientID string `json:"client_id"`
		ThreadID string `json:"thread_id"`
		Status   string `json:"status"`
	}
	out := []result{}
	for _, t := range b.Threads {
		id, created, err := s.Store.ImportThread(r.Context(), user(r), t)
		if err != nil {
			s.fail(w, r, storeErr(err, "Import"))
			return
		}
		st := "exists"
		if created {
			st = "created"
		}
		out = append(out, result{t.ClientID, id, st})
	}
	writeJSON(w, 200, map[string]any{"results": out})
}
