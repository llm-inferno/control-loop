package collector

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultPrometheusURL = "http://localhost:9090"
	defaultSATokenPath   = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultSACAPath      = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// errNoData signals that a query succeeded but returned an empty result (e.g. an
// idle window at cold start), distinct from a transport/query failure. Callers
// that treat "no traffic yet" as benign can check errors.Is(err, errNoData).
var errNoData = errors.New("no data returned from query")

// bearerRoundTripper injects an Authorization: Bearer <token> header (when a
// token is available) and delegates to the wrapped RoundTripper. When tokenPath
// is set it re-reads the file on every request, so a rotated projected
// service-account token (short TTL, refreshed on disk by the kubelet) does not
// go stale for a long-lived client. token is the fallback used when tokenPath is
// empty (tests) or a re-read transiently fails.
type bearerRoundTripper struct {
	token     string
	tokenPath string
	rt        http.RoundTripper
}

func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	token := b.token
	if b.tokenPath != "" {
		if data, err := os.ReadFile(b.tokenPath); err == nil {
			token = strings.TrimSpace(string(data))
		}
		// On a transient re-read failure fall back to the startup token (b.token).
		// Kubelet rewrites projected tokens via atomic rename, so reads effectively
		// never fail mid-flight; this is a defensive fallback, not a hot path.
	}
	if token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return b.rt.RoundTrip(req)
}

// newPromAPI builds a Prometheus v1.API from the environment. Defaults preserve
// server-sim behavior: no env => http://localhost:9090, no token, no custom TLS.
// For an HTTPS endpoint (e.g. OpenShift Thanos) it attaches a bearer token read
// from INFERNO_PROMETHEUS_TOKEN_PATH (default SA token) and trusts the CA at
// INFERNO_PROMETHEUS_CA_PATH (default SA CA), unless INFERNO_PROMETHEUS_INSECURE=true.
func newPromAPI() (v1.API, error) {
	url := os.Getenv(ctrl.PrometheusURLEnvName)
	if url == "" {
		url = defaultPrometheusURL
	}

	var base http.RoundTripper = api.DefaultRoundTripper
	if strings.HasPrefix(url, "https") {
		tlsCfg := &tls.Config{}
		if strings.EqualFold(os.Getenv(ctrl.PrometheusInsecureEnvName), "true") {
			tlsCfg.InsecureSkipVerify = true
		} else {
			caPath := os.Getenv(ctrl.PrometheusCAPathEnvName)
			explicitCA := caPath != ""
			if caPath == "" {
				caPath = defaultSACAPath
			}
			// A misconfigured CA that is silently ignored surfaces later as an
			// opaque "certificate signed by unknown authority" on every query.
			// So: an explicitly-configured CA that can't be read/parsed is a hard
			// error (fail fast); the implicit SA-CA default degrades to system
			// roots but logs the reason.
			pem, rerr := os.ReadFile(caPath)
			switch {
			case rerr != nil && explicitCA:
				return nil, fmt.Errorf("reading Prometheus CA %s: %w", caPath, rerr)
			case rerr != nil:
				fmt.Printf("Prometheus CA %s unreadable (%v); falling back to system roots\n", caPath, rerr)
			default:
				pool := x509.NewCertPool()
				if !pool.AppendCertsFromPEM(pem) {
					return nil, fmt.Errorf("Prometheus CA %s contains no valid certificates", caPath)
				}
				tlsCfg.RootCAs = pool
			}
		}
		base = &http.Transport{TLSClientConfig: tlsCfg}
	}

	// Resolve the bearer-token file. An explicitly-configured path that can't be
	// read is a hard error; the implicit SA-token default is optional (so
	// localhost/http keeps working off-cluster with no token). The token itself
	// is (re-)read per request by bearerRoundTripper so rotated SA tokens refresh.
	tokenPath := os.Getenv(ctrl.PrometheusTokenPathEnvName)
	explicit := tokenPath != ""
	if tokenPath == "" {
		tokenPath = defaultSATokenPath
	}
	var token string
	if b, err := os.ReadFile(tokenPath); err == nil {
		token = strings.TrimSpace(string(b))
	} else if explicit {
		return nil, fmt.Errorf("reading Prometheus token %s: %w", tokenPath, err)
	} else {
		tokenPath = "" // no SA token present; stay tokenless and skip per-request re-reads
	}

	client, err := api.NewClient(api.Config{
		Address:      url,
		RoundTripper: &bearerRoundTripper{token: token, tokenPath: tokenPath, rt: base},
	})
	if err != nil {
		return nil, fmt.Errorf("creating Prometheus client: %w", err)
	}
	return v1.NewAPI(client), nil
}

// queryVector runs an instant query and returns the full labeled vector.
func queryVector(ctx context.Context, apiv1 v1.API, query string) (model.Vector, error) {
	result, warnings, err := apiv1.Query(ctx, query, metav1.Now().Time)
	if err != nil {
		return nil, fmt.Errorf("querying Prometheus: %w", err)
	}
	if len(warnings) > 0 {
		fmt.Printf("Prometheus query warnings: %v\n", warnings)
	}
	vector, ok := result.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("unexpected result type %T", result)
	}
	return vector, nil
}

// queryScalar runs an instant query and returns the first sample's value.
func queryScalar(ctx context.Context, apiv1 v1.API, query string) (float64, error) {
	vector, err := queryVector(ctx, apiv1, query)
	if err != nil {
		return 0, err
	}
	if len(vector) == 0 {
		return 0, errNoData
	}
	return float64(vector[0].Value), nil
}
