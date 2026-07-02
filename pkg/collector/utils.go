package collector

import (
	"context"
)

// PrometheusQuery executes a PromQL query against the env-configured Prometheus
// endpoint and returns the first sample's value. Kept for the server-sim sense
// path; the endpoint/auth are configured via INFERNO_PROMETHEUS_* (defaults to
// http://localhost:9090 with no auth).
func PrometheusQuery(query string) (float64, error) {
	apiv1, err := newPromAPI()
	if err != nil {
		return 0, err
	}
	return queryScalar(context.Background(), apiv1, query)
}
