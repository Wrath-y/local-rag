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
