package observability

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// defaultBuckets are the duration buckets of every histogram in the registry.
// They are the conventional Prometheus set: fine enough around the tens of
// milliseconds of a page and the seconds of a slow request, coarse enough
// that one series stays bounded.
var defaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// metricKind distinguishes the three shapes one family can hold.
type metricKind uint8

const (
	kindCounter metricKind = iota + 1
	kindGauge
	kindHistogram
)

// Metrics is the process metrics surface: named families with fixed label
// sets, registered by their owners (transport, jobs, pool, telemetry) and
// rendered at exposition time in the Prometheus text format. It is the USE
// and RED vocabulary of the process; nothing here uploads anywhere.
type Metrics struct {
	clock ports.Clock

	mu       sync.RWMutex
	order    []string
	families map[string]*family

	httpOnce sync.Once
	inFlight atomic.Int64
}

// family is one metric name with its metadata and its labeled series.
type family struct {
	name     string
	help     string
	kind     metricKind
	typeName string

	mu         sync.RWMutex
	counters   map[string]*counterSeries
	histograms map[string]*histogramSeries
	read       func() []GaugeSample
}

// counterSeries is one label set of a counter family.
type counterSeries struct {
	key    string
	labels map[string]string
	value  *atomic.Uint64
}

// histogramSeries is one label set of a histogram family.
type histogramSeries struct {
	key       string
	labels    map[string]string
	histogram *Histogram
}

// Counter is a handle bound to one label set of one counter family. Methods
// are nil-safe so a zero value is inert.
type Counter struct {
	value *atomic.Uint64
}

// Inc adds one.
func (counter *Counter) Inc() {
	if counter == nil || counter.value == nil {
		return
	}
	counter.value.Add(1)
}

// Add adds delta.
func (counter *Counter) Add(delta uint64) {
	if counter == nil || counter.value == nil {
		return
	}
	counter.value.Add(delta)
}

// Histogram accumulates observed durations in seconds, in fixed buckets.
type Histogram struct {
	mu      sync.Mutex
	buckets []uint64
	count   uint64
	sum     float64
}

// Observe records one duration. A negative duration is clamped to zero: a
// clock that stepped backwards must not corrupt the sum.
func (histogram *Histogram) Observe(seconds float64) {
	if histogram == nil {
		return
	}
	if seconds < 0 {
		seconds = 0
	}
	histogram.mu.Lock()
	histogram.buckets[sort.SearchFloat64s(defaultBuckets, seconds)]++
	histogram.count++
	histogram.sum += seconds
	histogram.mu.Unlock()
}

// GaugeSample is one labeled reading of a live family.
type GaugeSample struct {
	// Labels identifies the series. Nil is the unlabeled series.
	Labels map[string]string
	// Value is the reading.
	Value float64
}

// NewMetrics builds the registry. The clock measures the request durations
// (the architecture gate refuses time.Now outside the clockseed package).
func NewMetrics(clock ports.Clock) *Metrics {
	return &Metrics{
		clock:    clock,
		families: make(map[string]*family),
	}
}

// Counter returns the handle of one label set of a counter family, creating
// it on first use. The label set must be small and fixed by the caller.
func (metrics *Metrics) Counter(name, help string, labels map[string]string) *Counter {
	if metrics == nil {
		return &Counter{}
	}
	metric := metrics.family(name, help, kindCounter)
	metric.mu.Lock()
	defer metric.mu.Unlock()
	key := labelKey(labels)
	series := metric.counters[key]
	if series == nil {
		series = &counterSeries{key: key, labels: cloneLabels(labels), value: &atomic.Uint64{}}
		metric.counters[key] = series
	}
	return &Counter{value: series.value}
}

// Histogram returns the handle of one label set of a histogram family,
// creating it on first use.
func (metrics *Metrics) Histogram(name, help string, labels map[string]string) *Histogram {
	if metrics == nil {
		return nil
	}
	metric := metrics.family(name, help, kindHistogram)
	metric.mu.Lock()
	defer metric.mu.Unlock()
	key := labelKey(labels)
	series := metric.histograms[key]
	if series == nil {
		series = &histogramSeries{
			key:       key,
			labels:    cloneLabels(labels),
			histogram: &Histogram{buckets: make([]uint64, len(defaultBuckets)+1)},
		}
		metric.histograms[key] = series
	}
	return series.histogram
}

// GaugeFunc registers a gauge whose samples are read live at exposition. The
// function must be cheap and must own its labels; it is called under the
// render lock, once per scrape.
func (metrics *Metrics) GaugeFunc(name, help string, read func() []GaugeSample) {
	metrics.live(name, help, "gauge", read)
}

// CounterFunc registers a cumulative counter whose total is read live at
// exposition. It exists for values a library already accumulates (the
// database pool), so the process does not keep a second copy that drifts.
func (metrics *Metrics) CounterFunc(name, help string, read func() float64) {
	metrics.live(name, help, "counter", func() []GaugeSample {
		return []GaugeSample{{Value: read()}}
	})
}

// live registers a family read at exposition.
func (metrics *Metrics) live(name, help, typeName string, read func() []GaugeSample) {
	if metrics == nil {
		return
	}
	metric := metrics.family(name, help, kindGauge)
	metric.mu.Lock()
	defer metric.mu.Unlock()
	if metric.read != nil && metric.typeName != typeName {
		panic(fmt.Sprintf("observability: metric %q is already registered as %s", name, metric.typeName))
	}
	if metric.read == nil {
		metric.read = read
	}
	metric.typeName = typeName
}

// family fetches or creates the family of one name, refusing a name that
// changes shape: one metric rendered as two types is an invalid exposition.
func (metrics *Metrics) family(name, help string, kind metricKind) *family {
	metrics.mu.RLock()
	metric, exists := metrics.families[name]
	metrics.mu.RUnlock()
	if exists {
		if metric.kind != kind {
			panic(fmt.Sprintf("observability: metric %q is already registered with another shape", name))
		}
		return metric
	}

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metric, exists := metrics.families[name]; exists {
		if metric.kind != kind {
			panic(fmt.Sprintf("observability: metric %q is already registered with another shape", name))
		}
		return metric
	}
	metric = &family{
		name:     name,
		help:     help,
		kind:     kind,
		typeName: typeNameOf(kind),
	}
	switch kind {
	case kindCounter:
		metric.counters = make(map[string]*counterSeries)
	case kindHistogram:
		metric.histograms = make(map[string]*histogramSeries)
	}
	metrics.families[name] = metric
	metrics.order = append(metrics.order, name)
	return metric
}

// typeNameOf renders the Prometheus type of a family kind.
func typeNameOf(kind metricKind) string {
	switch kind {
	case kindCounter:
		return "counter"
	case kindHistogram:
		return "histogram"
	default:
		return "gauge"
	}
}

// Render writes the registry in the Prometheus text format. Output is
// deterministic for identical values: families in registration order, label
// sets sorted inside each family.
func (metrics *Metrics) Render() string {
	if metrics == nil {
		return ""
	}

	metrics.mu.RLock()
	families := make([]*family, 0, len(metrics.order))
	for _, name := range metrics.order {
		families = append(families, metrics.families[name])
	}
	metrics.mu.RUnlock()

	// Every live family is read before anything is written. A gauge read can
	// advance a counter another family renders (the queue reading counts its
	// own scrape failures), and a scrape that measured a failure must report
	// it now rather than one scrape later.
	samples := make(map[*family][]GaugeSample, len(families))
	for _, metric := range families {
		if metric.kind != kindGauge {
			continue
		}
		metric.mu.RLock()
		read := metric.read
		metric.mu.RUnlock()
		if read != nil {
			samples[metric] = read()
		}
	}

	var out strings.Builder
	for _, metric := range families {
		metric.render(&out, samples[metric])
	}
	return out.String()
}

// Handler serves one scrape.
func (metrics *Metrics) Handler() http.Handler {
	if metrics == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, metrics.Render())
	})
}

// render writes one family: metadata first, then its series. A gauge family
// writes the samples Render already read; the others ignore the argument.
func (metric *family) render(out *strings.Builder, gaugeSamples []GaugeSample) {
	fmt.Fprintf(out, "# HELP %s %s\n# TYPE %s %s\n", metric.name, escapeHelp(metric.help), metric.name, metric.typeName)

	switch metric.kind {
	case kindCounter:
		metric.mu.RLock()
		series := make([]*counterSeries, 0, len(metric.counters))
		for _, one := range metric.counters {
			series = append(series, one)
		}
		metric.mu.RUnlock()
		sort.Slice(series, func(left, right int) bool { return series[left].key < series[right].key })
		for _, one := range series {
			fmt.Fprintf(out, "%s%s %d\n", metric.name, renderLabels(one.labels), one.value.Load())
		}
	case kindHistogram:
		metric.mu.RLock()
		series := make([]*histogramSeries, 0, len(metric.histograms))
		for _, one := range metric.histograms {
			series = append(series, one)
		}
		metric.mu.RUnlock()
		sort.Slice(series, func(left, right int) bool { return series[left].key < series[right].key })
		for _, one := range series {
			renderHistogram(out, metric.name, one.labels, one.histogram)
		}
	case kindGauge:
		if gaugeSamples == nil {
			return
		}
		samples := gaugeSamples
		sort.SliceStable(samples, func(left, right int) bool {
			return labelKey(samples[left].Labels) < labelKey(samples[right].Labels)
		})
		for _, sample := range samples {
			fmt.Fprintf(out, "%s%s %s\n", metric.name, renderLabels(sample.Labels), formatFloat(sample.Value))
		}
	}
}

// renderHistogram writes one histogram series: cumulative buckets, then sum
// and count.
func renderHistogram(out *strings.Builder, name string, labels map[string]string, histogram *Histogram) {
	histogram.mu.Lock()
	buckets := append([]uint64(nil), histogram.buckets...)
	count := histogram.count
	sum := histogram.sum
	histogram.mu.Unlock()

	cumulative := uint64(0)
	for index, bound := range defaultBuckets {
		cumulative += buckets[index]
		fmt.Fprintf(out, "%s_bucket%s %d\n", name, renderLabels(labelsWith(labels, "le", formatFloat(bound))), cumulative)
	}
	cumulative += buckets[len(defaultBuckets)]
	fmt.Fprintf(out, "%s_bucket%s %d\n", name, renderLabels(labelsWith(labels, "le", "+Inf")), cumulative)
	fmt.Fprintf(out, "%s_sum%s %s\n", name, renderLabels(labels), formatFloat(sum))
	fmt.Fprintf(out, "%s_count%s %d\n", name, renderLabels(labels), count)
}

// labelsWith returns a copy of labels extended with one pair, so a caller
// can render a derived set without mutating the registered one.
func labelsWith(labels map[string]string, name, value string) map[string]string {
	extended := cloneLabels(labels)
	if extended == nil {
		extended = make(map[string]string, 1)
	}
	extended[name] = value
	return extended
}

// labelKey renders a fixed label map as its stable comparison key. NUL
// separates the pairs so no value can imitate another pair's boundary.
func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	var out strings.Builder
	for _, name := range names {
		out.WriteString(name)
		out.WriteByte('=')
		out.WriteString(labels[name])
		out.WriteByte(0)
	}
	return out.String()
}

// renderLabels renders one label set in canonical order.
func renderLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)

	var out strings.Builder
	out.WriteByte('{')
	for index, name := range names {
		if index > 0 {
			out.WriteByte(',')
		}
		fmt.Fprintf(&out, "%s=%q", name, labels[name])
	}
	out.WriteByte('}')
	return out.String()
}

// cloneLabels copies a label set so a caller cannot mutate a registered
// series behind the registry's back.
func cloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	for name, value := range labels {
		cloned[name] = value
	}
	return cloned
}

// escapeHelp escapes the two characters the exposition format reserves in a
// HELP line.
func escapeHelp(help string) string {
	help = strings.ReplaceAll(help, `\`, `\\`)
	return strings.ReplaceAll(help, "\n", `\n`)
}

// formatFloat renders one Prometheus sample value.
func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
