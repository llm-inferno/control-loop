package collector

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PrometheusQuery executes a PromQL query and returns the result.
func PrometheusQuery(query string) (float64, error) {
	client, err := api.NewClient(api.Config{
		Address: "http://localhost:9090", // Update this with your Prometheus endpoint
	})
	if err != nil {
		return 0, fmt.Errorf("error creating Prometheus client: %v", err)
	}

	v1api := v1.NewAPI(client)
	result, warnings, err := v1api.Query(context.Background(), query, metav1.Now().Time)
	if err != nil {
		return 0, fmt.Errorf("error querying Prometheus: %v", err)
	}
	if len(warnings) > 0 {
		fmt.Printf("Prometheus query warnings: %v\n", warnings)
	}

	// Extract the result as a float64
	vector, ok := result.(model.Vector)
	if !ok || len(vector) == 0 {
		return 0, fmt.Errorf("no data returned from query")
	}
	return float64(vector[0].Value), nil
}
