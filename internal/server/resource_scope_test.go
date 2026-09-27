package server

import (
	"context"
	"net/http"
)

// withTestResourceScope lets focused handler tests exercise the same explicit
// owner boundary injected by the HTTP router without also testing middleware.
func withTestResourceScope(r *http.Request, ownerUserID string) *http.Request {
	ctx := context.WithValue(r.Context(), resourceScopeContextKey{}, ResourceScope{OwnerUserID: ownerUserID})
	return r.WithContext(ctx)
}
