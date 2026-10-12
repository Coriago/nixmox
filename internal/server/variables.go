package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"

	"github.com/go-chi/chi/v5"
	"nixmox/internal/statefile"
)

var variableName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)

func (s *Server) variables(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "nodeID")
	if s.forward(w, r, id) {
		return
	}
	path := s.Config.DataDirPath("inputs.json")
	if s.Config.InputsFile != "" {
		path = s.Config.InputsFile
	}
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if r.Method == "GET" {
		v := map[string]any{}
		b, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			http.Error(w, err.Error(), 500)
			return
		}
		if len(b) > 0 {
			if err = json.Unmarshal(b, &v); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		respond(w, 200, v)
		return
	}
	if !s.Config.AllowConfigWrite {
		http.Error(w, "config write disabled", 403)
		return
	}
	var v map[string]any
	if err := decode(r, &v); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if v == nil || len(v) > 128 {
		http.Error(w, "expected at most 128 named scalar inputs", 400)
		return
	}
	for key, value := range v {
		if !variableName.MatchString(key) {
			http.Error(w, "invalid variable name", 400)
			return
		}
		switch value.(type) {
		case float64, bool, string:
		default:
			http.Error(w, "inputs must be numbers, booleans or strings", 400)
			return
		}
	}
	if err := statefile.Write(path, v); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	respond(w, 200, v)
}
