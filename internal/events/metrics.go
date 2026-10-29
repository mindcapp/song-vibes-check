package events

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Values of the "reason" label of events_publish_errors_total.
const (
	reasonBufferFull = "buffer_full"
	reasonClosed     = "closed"
	reasonEncode     = "encode"
	reasonDelivery   = "delivery"
)

var (
	publishedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "events_published_total",
		Help: "Events acknowledged by the broker.",
	})

	publishErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "events_publish_errors_total",
		Help: "Events that were not delivered, by reason: buffer_full, closed, encode, delivery.",
	}, []string{"reason"})
)
