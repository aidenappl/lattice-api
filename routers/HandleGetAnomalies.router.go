package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/healthscan"
	"github.com/aidenappl/lattice-api/responder"
)

var HealthScanner *healthscan.Scanner

func HandleGetAnomalies(w http.ResponseWriter, r *http.Request) {
	if HealthScanner == nil {
		responder.New(w, []any{}, "no scanner configured")
		return
	}
	anomalies := HealthScanner.GetAnomalies()
	if anomalies == nil {
		anomalies = []healthscan.Anomaly{}
	}
	responder.New(w, anomalies, "anomalies retrieved")
}
