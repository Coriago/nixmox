package server

import "net/http"

type Middleware interface {
	Wrap(http.Handler) http.Handler
}

type NoopAuth struct{}

func (n NoopAuth) Wrap(next http.Handler) http.Handler {
	return next
}
