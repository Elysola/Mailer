package app

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// metricKind is the Prometheus type a family is rendered as.
type metricKind int

const (
	kindCounter metricKind = iota
	kindGauge
	kindHistogram
)

// defaultBuckets mirrors the Prometheus client default, spanning 5ms to 5min —
// the range a poll or a notification send actually lives in.
var defaultBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
	1, 2.5, 5, 10, 30, 60, 120, 300,
}

// metricDef is the static part of a family: what it means and how it renders.
// Declaring the whole set in one place is what makes HELP/TYPE correct; the
// scrape output is worthless if a dashboard cannot trust the units.
type metricDef struct {
	kind metricKind
	help string
}

var metricDefs = map[string]metricDef{
	"mailer_messages_fetched_total": {
		kindCounter, "Number of messages retrieved from IMAP.",
	},
	"mailer_messages_delivered_total": {
		kindCounter, "Number of notifications delivered successfully.",
	},
	"mailer_poll_failures_total": {
		kindCounter, "Number of poll cycles that failed.",
	},
	"mailer_auth_failures_total": {
		kindCounter, "Number of IMAP authentication failures.",
	},
	"mailer_notify_dropped_total": {
		kindCounter, "Number of queued notifications abandoned without delivery.",
	},
	"mailer_notify_rate_limited_total": {
		kindCounter, "Number of notification attempts rejected by a platform rate limit.",
	},
	"mailer_poll_duration_seconds": {
		kindHistogram, "Time spent in one poll cycle, including delivery.",
	},
	"mailer_notify_duration_seconds": {
		kindHistogram, "Time spent in a single notifier Send call.",
	},
	"mailer_last_poll_duration_seconds": {
		kindGauge, "Duration of the most recent successful poll cycle in seconds.",
	},
	"mailer_last_poll_success_timestamp": {
		kindGauge, "Unix timestamp of the most recent successful poll cycle.",
	},
	"mailer_pending_notifications": {
		kindGauge, "Notifications currently waiting in the retry queue.",
	},
	"mailer_uptime_seconds": {
		kindGauge, "Seconds since this process started.",
	},
}

// histogram is one labelled histogram sample.
type histogram struct {
	counts []uint64
	sum    float64
	count  uint64
}

func newHistogram(nBuckets int) *histogram {
	return &histogram{counts: make([]uint64, nBuckets)}
}

func (h *histogram) observe(v float64, buckets []float64) {
	// Buckets are cumulative on output, so incrementing only the first bucket
	// the value falls into and summing on render is cheaper than touching all
	// of them per observation. The +Inf bucket is the total count.
	for i, upper := range buckets {
		if v <= upper {
			h.counts[i]++
			break
		}
	}
	h.sum += v
	h.count++
}

// sample is one labelled time series.
type sample struct {
	value float64
	hist  *histogram
}

// family is a metric name plus its labelled samples.
type family struct {
	def     metricDef
	buckets []float64
	samples map[string]*sample
}

// Metrics is a small hand-rolled Prometheus registry.
//
// A dependency-free implementation keeps the daemon free of the client_golang
// tree (and its go.sum surface), at the cost of re-implementing the text
// exposition format — which is small enough to be worth it here.
type Metrics struct {
	mu       sync.Mutex
	families map[string]*family
	start    time.Time
}

func NewMetrics() *Metrics {
	return &Metrics{
		families: make(map[string]*family),
		start:    time.Now(),
	}
}

// resolve returns the family for name, creating it on first use. An
// unregistered name falls back to a counter so ad-hoc metrics still work
// instead of being silently dropped.
func (m *Metrics) resolve(name string, fallback metricKind) *family {
	f, ok := m.families[name]
	if ok {
		return f
	}
	def, ok := metricDefs[name]
	if !ok {
		def = metricDef{kind: fallback, help: "Unregistered metric."}
	}
	f = &family{def: def, samples: make(map[string]*sample)}
	if def.kind == kindHistogram {
		f.buckets = defaultBuckets
	}
	m.families[name] = f
	return f
}

func (m *Metrics) sample(f *family, labels map[string]string) *sample {
	key := labelKey(labels)
	s, ok := f.samples[key]
	if !ok {
		s = &sample{}
		if f.def.kind == kindHistogram {
			s.hist = newHistogram(len(f.buckets))
		}
		f.samples[key] = s
	}
	return s
}

func (m *Metrics) Inc(name string, labels map[string]string) {
	m.Add(name, labels, 1)
}

func (m *Metrics) Add(name string, labels map[string]string, delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sample(m.resolve(name, kindCounter), labels).value += float64(delta)
}

// Observe records a value. A name registered as a histogram accumulates it into
// buckets; anything else is set as a gauge. Call sites therefore do not need to
// know which one a metric is, and promoting a gauge to a histogram is a change
// to metricDefs alone.
func (m *Metrics) Observe(name string, labels map[string]string, val float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.resolve(name, kindGauge)
	s := m.sample(f, labels)
	if f.def.kind == kindHistogram {
		s.hist.observe(val, f.buckets)
		return
	}
	s.value = val
}

// Snapshot renders the registry in Prometheus text exposition format.
func (m *Metrics) Snapshot() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.setGaugeLocked("mailer_uptime_seconds", nil, time.Since(m.start).Seconds())

	names := make([]string, 0, len(m.families))
	for name := range m.families {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		renderFamily(&b, name, m.families[name])
	}
	renderGoStats(&b, m.start)
	return b.String()
}

func (m *Metrics) setGaugeLocked(name string, labels map[string]string, v float64) {
	f := m.resolve(name, kindGauge)
	m.sample(f, labels).value = v
}

func renderFamily(b *strings.Builder, name string, f *family) {
	// HELP and TYPE are emitted once per name. Repeating them per label set —
	// the obvious mistake — makes a real Prometheus scraper reject the whole
	// scrape with "second TYPE line for metric", so all label sets share one
	// header block.
	fmt.Fprintf(b, "# HELP %s %s\n", name, escapeHelp(f.def.help))
	switch f.def.kind {
	case kindHistogram:
		fmt.Fprintf(b, "# TYPE %s histogram\n", name)
	case kindGauge:
		fmt.Fprintf(b, "# TYPE %s gauge\n", name)
	default:
		fmt.Fprintf(b, "# TYPE %s counter\n", name)
	}

	keys := make([]string, 0, len(f.samples))
	for k := range f.samples {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		s := f.samples[k]
		switch f.def.kind {
		case kindHistogram:
			renderHistogram(b, name, k, s.hist, f.buckets)
		case kindGauge:
			fmt.Fprintf(b, "%s%s %s\n", name, brace(k), formatFloat(s.value))
		default:
			fmt.Fprintf(b, "%s%s %s\n", name, brace(k), formatFloat(s.value))
		}
	}
}

func renderHistogram(b *strings.Builder, name, labels string, h *histogram, buckets []float64) {
	var cumulative uint64
	for i, upper := range buckets {
		cumulative += h.counts[i]
		fmt.Fprintf(b, "%s_bucket%s %d\n", name, braceWith(labels, "le", formatFloat(upper)), cumulative)
	}
	fmt.Fprintf(b, "%s_bucket%s %d\n", name, braceWith(labels, "le", "+Inf"), h.count)
	fmt.Fprintf(b, "%s_sum%s %s\n", name, brace(labels), formatFloat(h.sum))
	fmt.Fprintf(b, "%s_count%s %d\n", name, brace(labels), h.count)
}

// renderGoStats emits the runtime metrics an operator needs to tell "the mail
// stopped arriving" apart from "the process is out of memory or leaking
// goroutines". No external collector package is pulled in for this.
func renderGoStats(b *strings.Builder, start time.Time) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	writeGoGauge(b, "go_goroutines", "Number of goroutines that currently exist.", float64(runtime.NumGoroutine()))
	writeGoGauge(b, "go_threads", "Number of OS threads created.", float64(runtime.NumCPU()))
	writeGoGauge(b, "go_memstats_alloc_bytes", "Bytes of allocated heap objects.", float64(ms.Alloc))
	writeGoGauge(b, "go_memstats_heap_alloc_bytes", "Bytes of allocated heap objects (heap).", float64(ms.HeapAlloc))
	writeGoGauge(b, "go_memstats_heap_inuse_bytes", "Bytes in in-use heap spans.", float64(ms.HeapInuse))
	writeGoGauge(b, "go_memstats_heap_objects", "Number of allocated heap objects.", float64(ms.HeapObjects))
	writeGoGauge(b, "go_memstats_stack_inuse_bytes", "Bytes in in-use stack spans.", float64(ms.StackInuse))
	writeGoGauge(b, "go_memstats_sys_bytes", "Bytes of memory obtained from the OS.", float64(ms.Sys))
	writeGoGauge(b, "go_memstats_gc_cpu_fraction", "Fraction of CPU time used by GC.", ms.GCCPUFraction)
	writeGoGauge(b, "go_gc_cycles_total", "Number of completed GC cycles.", float64(ms.NumGC))
	writeGoGauge(b, "process_start_time_seconds", "Start time of the process since unix epoch in seconds.", float64(start.Unix()))
}

func writeGoGauge(b *strings.Builder, name, help string, v float64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", name, help, name, name, formatFloat(v))
}

// labelKey normalises a label set into the "k=\"v\"" fragment used as a map key
// and rendered verbatim, sorted so two call sites passing the same labels in a
// different order share one time series.
func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		// The value is escaped here and dropped in verbatim by the renderer;
		// %q must not be used or the quotes and backslashes would be escaped
		// twice, producing a label no query could match.
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, escapeLabel(labels[k])))
	}
	return strings.Join(parts, ",")
}

func brace(labels string) string {
	if labels == "" {
		return ""
	}
	return "{" + labels + "}"
}

func braceWith(labels, k, v string) string {
	if labels == "" {
		return "{" + k + "=\"" + v + "\"}"
	}
	return "{" + labels + "," + k + "=\"" + v + "\"}"
}

// escapeLabel escapes the characters the exposition format reserves inside a
// label value. An account name is operator-supplied, so it must not be able to
// break the scrape.
func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

// formatFloat renders a value the way Prometheus expects: plain decimal, with
// integers without a trailing ".0" so counters do not look like floats.
func formatFloat(v float64) string {
	if v == float64(int64(v)) && v < 1e15 && v > -1e15 {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}
