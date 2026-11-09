package handler

import (
	"net/http"
)

// HealthHandler serves the liveness probe endpoint for our service.
// The reason why this endpoint uses the /healthz convention is to follow Kubernetes and cloud-native standards; moreover, this prevents route collisions with normal application endpoints that mightt serve medical or system data. Therefore, load balancers can poll this handler to verify that our HTTP listener is operational.
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
