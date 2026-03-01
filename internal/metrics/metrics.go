package metrics

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	reg *prometheus.Registry

	SMTPAcceptedRecipients prometheus.Counter
	SMTPRejectedMessages   *prometheus.CounterVec
	SMTPAuthFailures       prometheus.Counter

	QueueDepth *prometheus.GaugeVec

	DispatchBatchSize       prometheus.Histogram
	DispatchLatencySeconds  prometheus.Histogram
	DispatchAPICalls        *prometheus.CounterVec
	DispatchRetries         prometheus.Counter
	DispatchDLQ             prometheus.Counter
	RequeueStaleRecoveries  prometheus.Counter
	DispatchLastSuccessUnix atomic.Int64
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		SMTPAcceptedRecipients: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "relay_smtp_accepted_recipients_total",
			Help: "Total number of recipient messages accepted by SMTP and queued durably.",
		}),
		SMTPRejectedMessages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_smtp_rejected_messages_total",
			Help: "Total number of rejected SMTP messages partitioned by reason.",
		}, []string{"reason"}),
		SMTPAuthFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "relay_smtp_auth_failures_total",
			Help: "Total SMTP authentication failures.",
		}),
		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "relay_queue_depth",
			Help: "Current queue depth by status.",
		}, []string{"status"}),
		DispatchBatchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "relay_dispatch_batch_size",
			Help:    "Number of messages per outbound bulk API call.",
			Buckets: []float64{1, 10, 25, 50, 100, 250, 500},
		}),
		DispatchLatencySeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "relay_dispatch_latency_seconds",
			Help:    "Latency of MailerSend bulk API calls.",
			Buckets: prometheus.DefBuckets,
		}),
		DispatchAPICalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_dispatch_api_calls_total",
			Help: "Outbound API calls by result class and status code.",
		}, []string{"result", "status_code"}),
		DispatchRetries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "relay_dispatch_retries_total",
			Help: "Total retry transitions.",
		}),
		DispatchDLQ: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "relay_dispatch_dlq_total",
			Help: "Total DLQ transitions.",
		}),
		RequeueStaleRecoveries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "relay_requeue_stale_recoveries_total",
			Help: "Total stale processing recoveries on startup/runtime.",
		}),
	}

	reg.MustRegister(
		m.SMTPAcceptedRecipients,
		m.SMTPRejectedMessages,
		m.SMTPAuthFailures,
		m.QueueDepth,
		m.DispatchBatchSize,
		m.DispatchLatencySeconds,
		m.DispatchAPICalls,
		m.DispatchRetries,
		m.DispatchDLQ,
		m.RequeueStaleRecoveries,
	)
	return m
}

func (m *Metrics) Registry() *prometheus.Registry {
	return m.reg
}

