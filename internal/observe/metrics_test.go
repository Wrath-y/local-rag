package observe

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestIngestTotal_Increments(t *testing.T) {
	InitMetrics()

	IngestTotal.WithLabelValues("ok").Inc()
	IngestTotal.WithLabelValues("ok").Inc()
	IngestTotal.WithLabelValues("error").Inc()

	if got := testutil.ToFloat64(IngestTotal.WithLabelValues("ok")); got != 2 {
		t.Errorf("expected 2 ok increments, got %v", got)
	}
	if got := testutil.ToFloat64(IngestTotal.WithLabelValues("error")); got != 1 {
		t.Errorf("expected 1 error increment, got %v", got)
	}
}

func TestRender_ContainsMetricName(t *testing.T) {
	InitMetrics()

	IngestTotal.WithLabelValues("ok").Inc()

	output := string(Render())
	if !strings.Contains(output, "rag_ingest_total") {
		t.Errorf("expected Render() output to contain 'rag_ingest_total', got:\n%s", output)
	}
}
