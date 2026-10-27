// Package metrics holds the agent's Prometheus metrics, served on :8080/metrics.
//
// The first three use the same names as the original checkoutservice (:9090),
// so the canary dashboards compare v1 and v2 with one query split by the pod's
// version label. The rest only exist in the agent.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	OrdersPlaced = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "checkout_orders_placed_total",
		Help: "Total number of successfully placed orders.",
	}, []string{"currency"})

	OrderFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "checkout_orders_failed_total",
		Help: "Total number of failed PlaceOrder calls.",
	})

	// Wider buckets than v1: an LLM checkout takes seconds, not milliseconds.
	PlaceOrderDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "checkout_place_order_duration_seconds",
		Help:    "Latency of the PlaceOrder RPC.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300},
	})

	LLMCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "checkout_agent_llm_calls_total",
		Help: "Calls to the model, by result (ok, error).",
	}, []string{"result"})

	LLMTokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "checkout_agent_llm_tokens_total",
		Help: "Tokens processed by the model, by direction (input, output).",
	}, []string{"direction"})

	LLMSeconds = promauto.NewCounter(prometheus.CounterOpts{
		Name: "checkout_agent_llm_seconds_total",
		Help: "Time spent waiting for the model.",
	})

	Plans = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "checkout_agent_plans_total",
		Help: "Plans from the model, by outcome (valid, corrected, fallback, unchecked).",
	}, []string{"outcome"})

	// CorrectnessChecks counts the rule checks run on every order the agent
	// accepted: result is pass or fail, rule names the check.
	CorrectnessChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "checkout_agent_correctness_checks_total",
		Help: "Rule checks on accepted orders, by rule and result (pass, fail).",
	}, []string{"rule", "result"})

	// OrdersCorrect counts accepted orders by whether every rule passed.
	OrdersCorrect = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "checkout_agent_orders_checked_total",
		Help: "Accepted orders after the rule checks, by result (correct, incorrect).",
	}, []string{"result"})
)
