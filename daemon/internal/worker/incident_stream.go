package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// defaultIncidentWindow bounds how long the stream consumer reads before reporting. The daemon
// replays its recorded event backlog on connect, so a short window captures the recorded incidents
// and a moment of live stream; the window only matters for the never-ending live tail.
const defaultIncidentWindow = 3 * time.Second

const maxIncidentFrameBytes = 1 << 20

// StreamIncidentSource reads the daemon's H2 event stream as an ordinary HTTP client — no internal
// import, so AT-16 holds (a worker may not name the store or the owner surface; it may speak HTTP
// to a URL like any external consumer). It is the SUBSTANCE of incident-watch: it reads incidents
// the system actually recorded, rather than synthesizing them.
type StreamIncidentSource struct {
	URL    string
	Token  string
	Window time.Duration
	Client *http.Client
}

// streamFrame is the subset of the H2 SSE envelope this monitor reads.
type streamFrame struct {
	Kind  string `json:"kind"`
	Event struct {
		Kind     string `json:"kind"`
		JobID    string `json:"jobId"`
		EntityID string `json:"entityId"`
		At       string `json:"at"`
	} `json:"event"`
}

// Watch connects, reads frames until the window elapses or the stream ends, and returns the
// incidents found. The empty-vs-broken contract lives here: a failure to connect (transport error
// or a non-200 status) returns an ERROR, while a successful connection that sees no incidents
// returns a Connected scan with none. Reaching the read deadline on a live stream is normal
// termination, not a failure — by then the connection has already been proven.
func (s StreamIncidentSource) Watch(ctx context.Context) (IncidentScan, error) {
	window := s.Window
	if window <= 0 {
		window = defaultIncidentWindow
	}
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return IncidentScan{}, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}

	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// BROKEN: could not reach the stream. Never a silent all-clear.
		return IncidentScan{}, fmt.Errorf("connect %s: %w", s.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IncidentScan{}, fmt.Errorf("stream %s returned status %d", s.URL, resp.StatusCode)
	}

	scan := IncidentScan{Connected: true, StreamURL: s.URL}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), maxIncidentFrameBytes)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // SSE id:/comment/blank lines carry no frame
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		scan.StreamScanned++
		var frame streamFrame
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			continue // tolerate keepalives / non-JSON lines
		}
		if frame.Kind != "event" {
			continue // snapshots and other envelopes are not incidents
		}
		if inc, ok := incidentFromEvent(frame.Event.Kind, frame.Event.JobID, frame.Event.EntityID, frame.Event.At); ok {
			scan.Incidents = append(scan.Incidents, inc)
		}
	}
	// sc.Err() here is the read hitting the window deadline on a still-open stream — expected
	// termination for a live monitor. The connection was already proven (Do + 200 above), so this
	// is a valid scan, not a failure. A genuine connect failure returned earlier.
	return scan, nil
}
