package observe

import (
	"bytes"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
)

var (
	registry *prometheus.Registry

	IngestTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_ingest_total",
			Help: "Total ingest requests",
		},
		[]string{"result"},
	)

	RetrieveTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_retrieve_total",
			Help: "Total retrieve requests",
		},
		[]string{"hit"},
	)

	RetrieveLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "rag_retrieve_latency_seconds",
			Help:    "Retrieve latency",
			Buckets: prometheus.DefBuckets,
		},
	)

	ChunkTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "rag_chunk_total",
			Help: "Current chunk count",
		},
	)

	QueryRewriteTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_query_rewrite_total",
			Help: "Query rewrite operations",
		},
		[]string{"strategy"},
	)

	QueryRewriteLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "rag_query_rewrite_latency_seconds",
			Help:    "Query rewrite latency",
			Buckets: prometheus.DefBuckets,
		},
	)

	BackupTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "rag_backup_total",
			Help: "Total backups",
		},
	)

	LastBackupTimestamp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "rag_last_backup_timestamp_seconds",
			Help: "Last backup timestamp",
		},
	)

	RestoreTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_restore_total",
			Help: "Total restore attempts by result",
		},
		[]string{"result"},
	)

	RestoreLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "rag_restore_duration_seconds",
			Help:    "Restore duration",
			Buckets: prometheus.DefBuckets,
		},
	)

	IndexRebuildTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "rag_index_rebuild_total", Help: "Index rebuild attempts by outcome"},
		[]string{"outcome"},
	)

	IndexRebuildDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{Name: "rag_index_rebuild_duration_seconds", Help: "Index rebuild duration", Buckets: prometheus.DefBuckets},
	)

	IndexRebuildProgress = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "rag_index_rebuild_progress", Help: "Progress ratio of the current or latest rebuild"},
	)

	IndexRebuildActive = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "rag_index_rebuild_active", Help: "Whether an index rebuild is currently active"},
	)

	HookOutcomesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "rag_hook_outcomes_total", Help: "Total enabled RAG hook attempts by terminal outcome"},
		[]string{"outcome"},
	)

	HookLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{Name: "rag_hook_latency_seconds", Help: "RAG hook attempt latency", Buckets: prometheus.DefBuckets},
	)

	AgentToolCallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "rag_agent_tool_calls_total", Help: "Agent tool calls by tool, outcome, and safe error category"},
		[]string{"tool", "outcome", "error_category"},
	)

	AgentToolLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Name: "rag_agent_tool_latency_seconds", Help: "Agent tool execution latency", Buckets: prometheus.DefBuckets},
		[]string{"tool"},
	)

	AgentTerminalTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "rag_agent_terminal_total", Help: "Agent requests by terminal outcome"},
		[]string{"outcome"},
	)
)

func InitMetrics() {
	registry = prometheus.NewRegistry()
	registry.MustRegister(
		IngestTotal,
		RetrieveTotal,
		RetrieveLatency,
		ChunkTotal,
		QueryRewriteTotal,
		QueryRewriteLatency,
		BackupTotal,
		LastBackupTimestamp,
		RestoreTotal,
		RestoreLatency,
		IndexRebuildTotal,
		IndexRebuildDuration,
		IndexRebuildProgress,
		IndexRebuildActive,
		HookOutcomesTotal,
		HookLatency,
		AgentToolCallsTotal,
		AgentToolLatency,
		AgentTerminalTotal,
	)
}

// Render returns all metrics in Prometheus text format.
func Render() []byte {
	mfs, err := registry.Gather()
	if err != nil {
		return nil
	}

	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			return nil
		}
	}
	return buf.Bytes()
}

// Handler returns an http.Handler that serves metrics from the dedicated registry.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
