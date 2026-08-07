package worker

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

// IncidentWatchKind is the Brain worker slot for the liveness + recorded-incident monitor.
const IncidentWatchKind = "incident-watch"

// streamIncidentKinds are the recorded incident-class event kinds the stream consumer watches for.
// Each is really emitted and stored by the daemon: freeze.engaged (freeze), advance.reverted
// (advancing), payment.failed (approval). This is the SUBSTANCE of the monitor — real incidents
// the system already recorded, not synthesized signals.
var streamIncidentKinds = map[string]bool{
	"freeze.engaged":   true,
	"advance.reverted": true,
	"payment.failed":   true,
}

// Incident is one thing worth alerting on, with the evidence it was derived from.
type Incident struct {
	Kind     string // the event kind, or a probe verdict ("endpoint-down")
	Source   string // "stream" (recorded incident) | "health" (probe)
	EntityID string // the job/entity the event referenced, or the endpoint probed
	Detail   string // evidence: the event timestamp, or the probe status/error
}

// IncidentScan is the measured observation. Connected is the crux of the empty-vs-broken
// distinction: the source returns an ERROR when it cannot reach its target, and a Connected scan
// with zero incidents when it reached the target and found nothing. A monitor that is silent when
// broken is worse than no monitor, so "all clear" and "could not connect" are never the same value.
type IncidentScan struct {
	Connected     bool
	StreamURL     string
	StreamScanned int // frames read from the stream — evidence the read was live
	Probed        int // endpoints probed
	Incidents     []Incident
}

// IncidentSource is the monitor's seam. A stream/health adapter supplies production observations;
// a deterministic adapter drives the variance test. Watch returns an error ONLY when the monitor
// could not observe (a broken connection) — never for the healthy "nothing wrong" case.
type IncidentSource interface {
	Watch(ctx context.Context) (IncidentScan, error)
}

// IncidentWatch reports recorded incidents and endpoint health to Brain. Operator tool: no
// customer, quote, or escrow, and no release/payment/advance/signing capability.
type IncidentWatch struct {
	source IncidentSource
}

// NewIncidentWatch constructs the worker around its read-only observation source.
func NewIncidentWatch(source IncidentSource) *IncidentWatch {
	return &IncidentWatch{source: source}
}

// Kind implements Worker.
func (*IncidentWatch) Kind() string { return IncidentWatchKind }

// Handle observes once and reports. A broken observation (the source could not connect) is a LOUD
// failure — it returns an error, never a silent "all clear". Purchase is accepted by the interface
// and never used; TestIncidentWatchNeverSpends proves it at runtime on top of AT-16.
func (w *IncidentWatch) Handle(ctx context.Context, assignment envelope.Envelope, report Report, _ Purchase) error {
	if w == nil || w.source == nil {
		return fmt.Errorf("incident-watch source is not configured")
	}
	var a Assignment
	if err := assignment.Decode(&a); err != nil {
		return err
	}

	scan, err := w.source.Watch(ctx)
	if err != nil {
		// BROKEN, not clear: a monitor that cannot observe must fail the job, never report calm.
		return fmt.Errorf("incident watch could not observe: %w", err)
	}
	if !scan.Connected {
		// Defense in depth: a source must set Connected when it returns nil. A scan that claims
		// neither an error nor a connection is a broken contract, treated as broken.
		return fmt.Errorf("incident watch returned an unconnected scan without an error")
	}

	progress, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerProgress,
		map[string]any{
			"stage":          "monitored",
			"connected":      true,
			"stream_scanned": scan.StreamScanned,
			"probed":         scan.Probed,
			"incidents":      len(scan.Incidents),
		})
	if err != nil {
		return err
	}
	if err := report(ctx, progress); err != nil {
		return err
	}

	final, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerReport, incidentReport(scan))
	if err != nil {
		return err
	}
	return report(ctx, final)
}

// incidentReport derives the deliverable. BOTH the all-clear and the incident report state the
// connection and how much was observed, so "0 incidents" is provably a healthy read rather than a
// dead monitor — the report a broken monitor never reaches (Handle errored instead).
func incidentReport(scan IncidentScan) envelope.Deliverable {
	observed := fmt.Sprintf("connected; read %d stream frame(s), probed %d endpoint(s)", scan.StreamScanned, scan.Probed)
	streamSrc := "stream:" + scan.StreamURL

	if len(scan.Incidents) == 0 {
		return envelope.Deliverable{
			Title:   "Incident watch",
			Summary: "All clear — " + observed + ", 0 incidents.",
			Claims: []envelope.Claim{{
				Text:    "no incident-class events recorded and no endpoint failing its probe (" + observed + ")",
				Sources: []string{streamSrc},
			}},
			Sources:    []string{streamSrc},
			Disclaimer: incidentDisclaimer,
		}
	}

	// Stable order: source, then kind, then entity.
	incidents := append([]Incident(nil), scan.Incidents...)
	sort.SliceStable(incidents, func(i, j int) bool {
		if incidents[i].Source != incidents[j].Source {
			return incidents[i].Source < incidents[j].Source
		}
		if incidents[i].Kind != incidents[j].Kind {
			return incidents[i].Kind < incidents[j].Kind
		}
		return incidents[i].EntityID < incidents[j].EntityID
	})

	claims := make([]envelope.Claim, 0, len(incidents))
	sources := []string{streamSrc}
	for _, inc := range incidents {
		src := inc.Source + ":" + inc.EntityID
		sources = append(sources, src)
		claims = append(claims, envelope.Claim{
			Text:    fmt.Sprintf("%s [%s] %s — %s", inc.Kind, inc.Source, inc.EntityID, inc.Detail),
			Sources: []string{src},
		})
	}
	return envelope.Deliverable{
		Title:      "Incident watch",
		Summary:    fmt.Sprintf("%d incident(s) — %s.", len(incidents), observed),
		Claims:     claims,
		Sources:    sources,
		Disclaimer: incidentDisclaimer,
	}
}

const incidentDisclaimer = "Liveness + recorded-incident monitor: it reports incident-class events already recorded on the " +
	"H2 stream (freeze.engaged, advance.reverted, payment.failed) and endpoints failing a health probe. " +
	"It is not anomaly detection, and it authorizes nothing."

// combinedIncidentSource runs the stream consumer (the substance) and, if present, the health
// probe (a second signal), merging their incidents. The stream's failure is the monitor's failure
// — if it cannot reach the event stream it is broken and Watch errors. A health-probe fault
// surfaces too; a merely-down endpoint is an incident, not a fault.
type combinedIncidentSource struct {
	stream IncidentSource
	health IncidentSource
}

// NewCombinedIncidentSource wires the stream consumer with an optional health probe.
func NewCombinedIncidentSource(stream, health IncidentSource) IncidentSource {
	return combinedIncidentSource{stream: stream, health: health}
}

func (c combinedIncidentSource) Watch(ctx context.Context) (IncidentScan, error) {
	scan, err := c.stream.Watch(ctx)
	if err != nil {
		return IncidentScan{}, err
	}
	if c.health != nil {
		h, herr := c.health.Watch(ctx)
		if herr != nil {
			return IncidentScan{}, herr
		}
		scan.Probed = h.Probed
		scan.Incidents = append(scan.Incidents, h.Incidents...)
	}
	return scan, nil
}

// incidentFromEvent maps a stream event to an incident when its kind is one this monitor watches.
func incidentFromEvent(kind, jobID, entityID, at string) (Incident, bool) {
	if !streamIncidentKinds[kind] {
		return Incident{}, false
	}
	id := strings.TrimSpace(jobID)
	if id == "" {
		id = strings.TrimSpace(entityID)
	}
	detail := "recorded"
	if at != "" {
		detail = "at " + at
	}
	return Incident{Kind: kind, Source: "stream", EntityID: id, Detail: detail}, true
}
