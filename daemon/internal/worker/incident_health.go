package worker

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// defaultProbeTimeout bounds each endpoint probe.
const defaultProbeTimeout = 2 * time.Second

// HealthProbeSource is the SECOND signal: it probes configured endpoints over HTTP and flags the
// ones that are unreachable or returning a server error. It deliberately does NOT flag 4xx: a 4xx
// means the server is alive and answering (the sidecar's paid endpoint returns 402 by design), so
// treating it as down would be a false positive. On its own a probe only proves a port answers —
// which is why the stream consumer is the substance and this is the supplement.
type HealthProbeSource struct {
	Endpoints []string
	Timeout   time.Duration
	Client    *http.Client
}

// Watch probes every endpoint. Probing always "connects" in the monitor sense — the worker ran —
// so a down endpoint is an incident, not a fault. Connected is therefore true whenever the probe
// executed; an empty Endpoints list is a valid no-op that reports zero probes.
func (s HealthProbeSource) Watch(ctx context.Context) (IncidentScan, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	scan := IncidentScan{Connected: true}
	for _, endpoint := range s.Endpoints {
		scan.Probed++
		pctx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(pctx, http.MethodGet, endpoint, nil)
		if err != nil {
			cancel()
			scan.Incidents = append(scan.Incidents, Incident{
				Kind: "endpoint-unreachable", Source: "health", EntityID: endpoint,
				Detail: "malformed endpoint: " + err.Error(),
			})
			continue
		}
		resp, err := client.Do(req)
		cancel()
		if err != nil {
			scan.Incidents = append(scan.Incidents, Incident{
				Kind: "endpoint-unreachable", Source: "health", EntityID: endpoint,
				Detail: "no response: " + err.Error(),
			})
			continue
		}
		_ = resp.Body.Close()
		// 5xx is a real liveness incident; 4xx (401/402/404) means the server is up and refusing,
		// which is not down. This is the "only proves a port is open" limit, made explicit.
		if resp.StatusCode >= 500 {
			scan.Incidents = append(scan.Incidents, Incident{
				Kind: "endpoint-down", Source: "health", EntityID: endpoint,
				Detail: fmt.Sprintf("status %d", resp.StatusCode),
			})
		}
	}
	return scan, nil
}
