package handlers

import (
	"context"
	"net/http"
)

func contextWith(r *http.Request, id string) context.Context {
	return context.WithValue(r.Context(), ctxKey{}, id)
}
