package agent

// LKW checkpoints and RIP tracking for fault injection.
//
// LKW (last known well): every stage of the checkout that really completes
// records a checkpoint. A checkpoint missing from a run (steps_lost) shows
// where the fault stopped or skipped the real work. Faked tool calls do not
// record their checkpoint, because the real work never happened.
//
// RIP (reachability, infection, propagation): the moment a fault mode changes
// what the agent does or sees, the run is marked infected at that stage
// (infection_point). Every checkpoint recorded from then on, the infected stage
// included, carries the bad state forward (propagation_depth).

import (
	"context"
	"sync"
	"time"
)

// ExpectedCheckpoints is every checkpoint a correct order records, in order.
var ExpectedCheckpoints = []string{
	"TASK_START", "CART_LOADED", "PRODUCTS_LOADED", "PRICES_CONVERTED", "SHIPPING_QUOTED",
	"PAYMENT_CHARGED", "ORDER_SHIPPED", "CART_EMPTIED", "CONFIRMATION_SENT",
	"ORDER_VERIFIED", "FINAL_ANSWER",
}

// toolCheckpoint is the checkpoint a tool records when it really succeeds.
var toolCheckpoint = map[string]string{
	"get_cart":                "CART_LOADED",
	"get_product":             "PRODUCTS_LOADED",
	"convert_currency":        "PRICES_CONVERTED",
	"quote_shipping":          "SHIPPING_QUOTED",
	"charge_card":             "PAYMENT_CHARGED",
	"ship_order":              "ORDER_SHIPPED",
	"empty_cart":              "CART_EMPTIED",
	"send_order_confirmation": "CONFIRMATION_SENT",
}

// Checkpoint is one LKW entry.
type Checkpoint struct {
	Step     string `json:"step"`
	At       string `json:"ts"`
	Detail   string `json:"detail,omitempty"`
	Infected bool   `json:"infected"`
}

// RIP is the infection summary of one run.
type RIP struct {
	InfectionPoint   string `json:"infection_point"`
	InfectionDetail  string `json:"infection_detail,omitempty"`
	PropagationDepth int    `json:"propagation_depth"`
	LastKnownWell    string `json:"last_known_well"`
}

// Tracker records the checkpoints and the infection of one order. Its methods
// are safe on a nil Tracker, so the agent can call them unconditionally.
type Tracker struct {
	mu        sync.Mutex
	lkw       []Checkpoint
	seen      map[string]bool
	infection string
	detail    string
	depth     int
}

type trackerKey struct{}

// WithTracker attaches a new Tracker to ctx. PlaceOrderTraced adds one itself
// if the caller did not, so the fault runner only needs this to read it back.
func WithTracker(ctx context.Context) (context.Context, *Tracker) {
	t := &Tracker{seen: map[string]bool{}}
	return context.WithValue(ctx, trackerKey{}, t), t
}

func trackerFrom(ctx context.Context) *Tracker {
	t, _ := ctx.Value(trackerKey{}).(*Tracker)
	return t
}

// checkpoint records step once; the first call wins.
func (t *Tracker) checkpoint(step, detail string) {
	if t == nil || step == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen[step] {
		return
	}
	t.seen[step] = true
	infected := t.infection != ""
	if infected {
		t.depth++
	}
	t.lkw = append(t.lkw, Checkpoint{Step: step, At: time.Now().UTC().Format(time.RFC3339Nano),
		Detail: detail, Infected: infected})
}

// infect marks the run infected at point. Only the first infection counts.
func (t *Tracker) infect(point, detail string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.infection == "" {
		t.infection, t.detail = point, detail
	}
}

// LKW returns a copy of the checkpoints recorded so far.
func (t *Tracker) LKW() []Checkpoint {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Checkpoint(nil), t.lkw...)
}

// RIP returns the infection summary. LastKnownWell is the last checkpoint
// recorded before the infection (or the last one overall in a clean run).
func (t *Tracker) RIP() RIP {
	if t == nil {
		return RIP{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r := RIP{InfectionPoint: t.infection, InfectionDetail: t.detail, PropagationDepth: t.depth}
	for _, c := range t.lkw {
		if c.Infected {
			break
		}
		r.LastKnownWell = c.Step
	}
	return r
}
