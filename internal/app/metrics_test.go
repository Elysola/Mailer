package app

import (
	"regexp"
	"strings"
	"testing"
)

func linesOf(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

// TestSnapshotEmitsOneTypePerName is the regression guard for the bug the old
// implementation had: HELP/TYPE were printed once per *label set*, which makes a
// real Prometheus scraper reject the entire scrape with "second TYPE line for
// metric". Any family with two accounts triggered it.
func TestSnapshotEmitsOneTypePerName(t *testing.T) {
	m := NewMetrics()
	for _, acc := range []string{"a", "b", "c"} {
		m.Inc("mailer_messages_fetched_total", map[string]string{"account": acc})
	}

	types := map[string]int{}
	helps := map[string]int{}
	for _, line := range linesOf(m.Snapshot()) {
		if strings.HasPrefix(line, "# TYPE ") {
			types[strings.Fields(line)[2]]++
		}
		if strings.HasPrefix(line, "# HELP ") {
			helps[strings.Fields(line)[2]]++
		}
	}
	for name, n := range types {
		if n != 1 {
			t.Errorf("%d TYPE lines for %s, want 1", n, name)
		}
	}
	for name, n := range helps {
		if n != 1 {
			t.Errorf("%d HELP lines for %s, want 1", n, name)
		}
	}
	if types["mailer_messages_fetched_total"] != 1 {
		t.Error("mailer_messages_fetched_total has no TYPE line")
	}
}

func TestSnapshotCounterFormat(t *testing.T) {
	m := NewMetrics()
	m.Inc("mailer_messages_fetched_total", map[string]string{"account": "primary"})
	m.Add("mailer_messages_fetched_total", map[string]string{"account": "primary"}, 4)

	snap := m.Snapshot()
	if !strings.Contains(snap, "# TYPE mailer_messages_fetched_total counter") {
		t.Errorf("missing counter TYPE:\n%s", snap)
	}
	// Integers must render without a decimal point, the way Prometheus expects.
	if !strings.Contains(snap, `mailer_messages_fetched_total{account="primary"} 5`) {
		t.Errorf("counter value not rendered as an integer:\n%s", snap)
	}
}

func TestSnapshotGaugeFormat(t *testing.T) {
	m := NewMetrics()
	m.Observe("mailer_pending_notifications", map[string]string{"account": "a"}, 3)
	snap := m.Snapshot()
	if !strings.Contains(snap, "# TYPE mailer_pending_notifications gauge") {
		t.Errorf("missing gauge TYPE:\n%s", snap)
	}
	if !strings.Contains(snap, `mailer_pending_notifications{account="a"} 3`) {
		t.Errorf("gauge value missing:\n%s", snap)
	}
}

// TestObserveHistogramBuckets checks the bucket contract: cumulative counts, an
// explicit +Inf bucket, and _sum/_count. A dashboard computing a p95 depends on
// all three being right.
func TestObserveHistogramBuckets(t *testing.T) {
	m := NewMetrics()
	m.Observe("mailer_poll_duration_seconds", map[string]string{"account": "a"}, 0.003) // <= 0.005
	m.Observe("mailer_poll_duration_seconds", map[string]string{"account": "a"}, 0.02)  // <= 0.025
	m.Observe("mailer_poll_duration_seconds", map[string]string{"account": "a"}, 999.0) // +Inf only

	snap := m.Snapshot()
	if !strings.Contains(snap, "# TYPE mailer_poll_duration_seconds histogram") {
		t.Fatalf("missing histogram TYPE:\n%s", snap)
	}
	for _, want := range []string{
		`mailer_poll_duration_seconds_bucket{account="a",le="0.005"} 1`,
		`mailer_poll_duration_seconds_bucket{account="a",le="0.01"} 1`,
		`mailer_poll_duration_seconds_bucket{account="a",le="0.025"} 2`,
		`mailer_poll_duration_seconds_bucket{account="a",le="0.05"} 2`,
		`mailer_poll_duration_seconds_bucket{account="a",le="+Inf"} 3`,
		`mailer_poll_duration_seconds_count{account="a"} 3`,
	} {
		if !strings.Contains(snap, want) {
			t.Errorf("missing %q in:\n%s", want, snap)
		}
	}
	// The sum must be the real sum, not the bucket sum.
	if !strings.Contains(snap, `mailer_poll_duration_seconds_sum{account="a"} 999.023`) {
		t.Errorf("_sum is wrong:\n%s", snap)
	}
}

// TestSnapshottHasNoDuplicateSeries guards the other half of a valid scrape:
// two identical series lines are a parse error.
func TestSnapshotHasNoDuplicateSeries(t *testing.T) {
	m := NewMetrics()
	m.Inc("mailer_poll_failures_total", map[string]string{"account": "a"})
	m.Inc("mailer_poll_failures_total", map[string]string{"account": "b"})

	seen := map[string]bool{}
	for _, line := range linesOf(m.Snapshot()) {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := strings.Fields(line)[0]
		if seen[name] {
			t.Errorf("duplicate series %q", name)
		}
		seen[name] = true
	}
}

// TestLabelOrderIsNormalised: the same labels passed in a different order must
// land in one series, not two.
func TestLabelOrderIsNormalised(t *testing.T) {
	m := NewMetrics()
	m.Inc("mailer_messages_delivered_total", map[string]string{"account": "a", "notifier": "telegram"})
	m.Inc("mailer_messages_delivered_total", map[string]string{"notifier": "telegram", "account": "a"})

	snap := m.Snapshot()
	if n := strings.Count(snap, "mailer_messages_delivered_total{"); n != 1 {
		t.Errorf("%d series, want 1 (label order must not split them):\n%s", n, snap)
	}
	if !strings.Contains(snap, `mailer_messages_delivered_total{account="a",notifier="telegram"} 2`) {
		t.Errorf("value not accumulated:\n%s", snap)
	}
}

// TestLabelValuesAreEscaped: an account name is operator-supplied and must not
// be able to break the scrape.
func TestLabelValuesAreEscaped(t *testing.T) {
	m := NewMetrics()
	m.Inc("mailer_poll_failures_total", map[string]string{"account": `we"ird\name`})
	snap := m.Snapshot()
	if !strings.Contains(snap, `{account="we\"ird\\name"}`) {
		t.Errorf("label not escaped:\n%s", snap)
	}
	if strings.Contains(snap, `we"ird`) {
		t.Errorf("unescaped quote leaked:\n%s", snap)
	}
}

func TestSnapshotIncludesGoRuntimeStats(t *testing.T) {
	snap := NewMetrics().Snapshot()
	for _, want := range []string{
		"go_goroutines",
		"go_memstats_alloc_bytes",
		"go_memstats_heap_inuse_bytes",
		"process_start_time_seconds",
		"mailer_uptime_seconds",
	} {
		if !strings.Contains(snap, want) {
			t.Errorf("missing %q in:\n%s", want, snap)
		}
	}
}

// TestSnapshotIsDeterministic keeps diffs and tests stable: map iteration order
// must not leak into the output.
func TestSnapshotIsDeterministic(t *testing.T) {
	m := NewMetrics()
	for _, n := range []string{"a", "b", "c", "d"} {
		m.Inc("mailer_poll_failures_total", map[string]string{"account": n})
		m.Inc("mailer_auth_failures_total", map[string]string{"account": n})
	}
	first := m.Snapshot()
	for i := 0; i < 20; i++ {
		// Uptime and the runtime stats change between calls, so compare with
		// them filtered out.
		if got, want := stripVolatile(m.Snapshot()), stripVolatile(first); got != want {
			t.Fatalf("snapshot output is not deterministic:\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	}
}

// stripVolatile removes the values that legitimately change between two calls
// in the same process.
func stripVolatile(s string) string {
	re := regexp.MustCompile(`(?m)^(?:mailer_uptime_seconds|go_\w+|process_start_time_seconds) .*$`)
	return re.ReplaceAllString(s, "")
}

// TestUnregisteredMetricFallsBack: an ad-hoc metric that is not in metricDefs
// must still be exported rather than silently dropped.
func TestUnregisteredMetricFallsBack(t *testing.T) {
	m := NewMetrics()
	m.Inc("mailer_ad_hoc_total", nil)
	if !strings.Contains(m.Snapshot(), "mailer_ad_hoc_total 1") {
		t.Error("unregistered counter was dropped")
	}
}

func TestFormatFloat(t *testing.T) {
	cases := map[float64]string{
		0:     "0",
		1:     "1",
		1.5:   "1.5",
		0.005: "0.005",
		1234:  "1234",
		-2:    "-2",
	}
	for in, want := range cases {
		if got := formatFloat(in); got != want {
			t.Errorf("formatFloat(%v) = %q, want %q", in, got, want)
		}
	}
}
