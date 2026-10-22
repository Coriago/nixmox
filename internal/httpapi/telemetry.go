package httpapi

import (
	"net/http"

	"nixmox/internal/presentation"
)

func (a *Server) telemetryView(host string) (view, error) {
	sample, err := a.service.Telemetry(host)
	return view{TargetID: host, Telemetry: &sample, MetricRows: presentation.TelemetryRows(sample)}, err
}
func (a *Server) telemetryPage(w http.ResponseWriter, r *http.Request) {
	v, err := a.telemetryView(r.PathValue("id"))
	if err != nil {
		writeError(w, err, "")
		return
	}
	a.render(w, r, "network.html", 200, v)
}
func (a *Server) telemetryFragment(w http.ResponseWriter, r *http.Request) {
	v, err := a.telemetryView(r.PathValue("id"))
	if err != nil {
		writeError(w, err, "")
		return
	}
	a.render(w, r, "telemetry-status", 200, v)
}
