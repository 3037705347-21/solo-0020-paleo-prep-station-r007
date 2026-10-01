package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/station"
)

type API struct {
	service *station.Service
}

type workbenchMaintenanceRequest struct {
	InMaintenance bool   `json:"in_maintenance"`
	Reason        string `json:"reason"`
}

func NewRouter(service *station.Service) http.Handler {
	api := &API{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("GET /workbenches", api.listWorkbenches)
	mux.HandleFunc("POST /workbenches/{id}/maintenance", api.setWorkbenchMaintenance)
	mux.HandleFunc("GET /specimens", api.listSpecimens)
	mux.HandleFunc("POST /specimens", api.registerSpecimen)
	mux.HandleFunc("GET /specimens/{id}", api.getSpecimen)
	mux.HandleFunc("GET /specimens/{id}/plans", api.listPlansForSpecimen)
	mux.HandleFunc("POST /specimens/{id}/plans", api.createPlan)
	mux.HandleFunc("POST /specimens/{id}/release", api.releaseSpecimen)
	mux.HandleFunc("GET /plans/{id}", api.getPlan)
	mux.HandleFunc("POST /plans/{id}/approval", api.approvePlan)
	mux.HandleFunc("POST /plans/{id}/progress", api.logProgress)
	return requestIDMiddleware(mux)
}

func (api *API) listWorkbenches(w http.ResponseWriter, r *http.Request) {
	values, err := api.service.ListWorkbenches(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": values})
}

func (api *API) setWorkbenchMaintenance(w http.ResponseWriter, r *http.Request) {
	var request workbenchMaintenanceRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.SetWorkbenchMaintenance(
		r.Context(),
		r.PathValue("id"),
		request.InMaintenance,
		request.Reason,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
