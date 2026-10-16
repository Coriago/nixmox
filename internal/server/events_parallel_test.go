package server

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParallelFragmentsHaveIndependentRouteContext(t *testing.T) {
	s := operationServer(t)
	s.Mux.Get("/partials/check/{value}", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Millisecond)
		fmt.Fprint(w, chi.URLParam(r, "value"))
	})
	parent := httptest.NewRequest("GET", "/api/v1/events", nil)
	parent = parent.WithContext(context.WithValue(parent.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	paths := []string{}
	for i := range 8 {
		paths = append(paths, fmt.Sprintf("/partials/check/%d", i))
	}
	for range 10 {
		got := s.renderFragments(parent, paths)
		for i, path := range paths {
			if got[path].String() != fmt.Sprint(i) {
				t.Fatalf("wrong route for %s: %s", path, got[path].String())
			}
		}
	}
}
