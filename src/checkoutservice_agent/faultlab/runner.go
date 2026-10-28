// Package faultlab runs the checkout agent once per fault mode and reports,
// for each run, the LKW checkpoints reached and lost, the RIP infection point
// and propagation depth, and whether the fault was caught (by the agent's own
// verification or by the correctness rules). The /ui endpoints, the
// cmd/faulttest command and the tests all use it.
package faultlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"checkoutservice-agent/agent"
	pb "checkoutservice-agent/genproto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ResultsFile is the name of the results JSON in RESULTS_DIR.
const ResultsFile = "checkoutagent_fault_results.json"

// TestUser owns the test cart, so real carts are untouched.
const TestUser = "faultlab-checkout"

// TestItems is the cart used by every run. Two different products, so
// BL_ITEM_DROPPED and BL_DUPLICATE_ITEM have something to work on.
var TestItems = []*pb.CartItem{
	{ProductId: "OLJCESPC7Z", Quantity: 1},
	{ProductId: "66VCHSJNUP", Quantity: 2},
}

// Row is one fault mode's result, in the shape the FaultLab dashboard renders
// (the same keys as the other agents' test_fault_injection.py results).
type Row struct {
	FaultMode        string              `json:"fault_mode"`
	ElapsedMS        int64               `json:"elapsed_ms"`
	StepsReached     []string            `json:"steps_reached"`
	StepsLost        []string            `json:"steps_lost"`
	InfectionPoint   string              `json:"infection_point,omitempty"`
	InfectionDetail  string              `json:"infection_detail,omitempty"`
	PropagationDepth int                 `json:"propagation_depth"`
	LastKnownWell    string              `json:"last_known_well"`
	OrderAccepted    bool                `json:"order_accepted"`
	TrackingID       string              `json:"tracking_id,omitempty"`
	DetectedBy       string              `json:"detected_by,omitempty"`
	FailedChecks     []string            `json:"failed_checks,omitempty"`
	Checks           []agent.CheckResult `json:"checks,omitempty"`
	BoundaryAlert    string              `json:"boundary_alert_point,omitempty"`
	Outcome          string              `json:"outcome"`
	LKW              []agent.Checkpoint  `json:"lkw"`
	Trace            []agent.Step        `json:"trace"`
	Error            string              `json:"error,omitempty"`
}

// Results is the whole run, as saved to ResultsFile and served by /ui/results.
type Results struct {
	Agent       string `json:"agent"`
	Backend     string `json:"backend"`
	GeneratedAt string `json:"generated_at"`
	Results     []Row  `json:"results"`
}

// Runner places one test order per fault mode.
type Runner struct {
	Agent    *agent.CheckoutAgent
	Cart     pb.CartServiceClient
	Currency string // user currency of the test order (default USD)
	Backend  string // "live" or "mock", recorded in the results
	Logf     func(format string, args ...any)
	Timeout  time.Duration // per mode (default 10 minutes)
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// RunAll runs NONE first (the baseline), then every other mode in modes.
func (r *Runner) RunAll(modes []string) Results {
	modes = append([]string{"NONE"}, without(modes, "NONE")...)
	res := Results{Agent: "checkoutservice", Backend: r.Backend}
	for _, m := range modes {
		r.logf("== %s", m)
		row := r.RunOne(m)
		r.logf("   accepted=%v infection=%q depth=%d lkw=%s lost=%v detected=%q %s",
			row.OrderAccepted, row.InfectionPoint, row.PropagationDepth, row.LastKnownWell,
			row.StepsLost, row.DetectedBy, row.Outcome)
		res.Results = append(res.Results, row)
	}
	res.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	return res
}

// RunOne seeds the test cart and places one order under mode.
func (r *Runner) RunOne(mode string) Row {
	row := Row{FaultMode: mode, StepsReached: []string{}, StepsLost: []string{}}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, _ = r.Cart.EmptyCart(ctx, &pb.EmptyCartRequest{UserId: TestUser})
	for _, it := range TestItems {
		if _, err := r.Cart.AddItem(ctx, &pb.AddItemRequest{UserId: TestUser, Item: it}); err != nil {
			row.Error = "seeding cart: " + err.Error()
			return row
		}
	}

	currency := r.Currency
	if currency == "" {
		currency = "USD"
	}
	req := &pb.PlaceOrderRequest{
		UserId:       TestUser,
		UserCurrency: currency,
		Email:        "faultlab@example.com",
		Address: &pb.Address{StreetAddress: "1600 Amphitheatre Parkway", City: "Mountain View",
			State: "CA", Country: "United States", ZipCode: 94043},
		CreditCard: &pb.CreditCardInfo{CreditCardNumber: "4432-8015-6152-0454",
			CreditCardCvv: 672, CreditCardExpirationYear: 2039, CreditCardExpirationMonth: 1},
	}
	ctx, tr := agent.WithTracker(agent.WithFaultMode(ctx, mode))

	start := time.Now()
	resp, steps, err := r.Agent.PlaceOrderTraced(ctx, req)
	row.ElapsedMS = time.Since(start).Milliseconds()
	row.Trace = steps

	// LKW: which checkpoints this run reached, and which it lost.
	row.LKW = tr.LKW()
	reached := map[string]bool{}
	for _, c := range row.LKW {
		reached[c.Step] = true
		row.StepsReached = append(row.StepsReached, c.Step)
	}
	for _, e := range agent.ExpectedCheckpoints {
		if !reached[e] {
			row.StepsLost = append(row.StepsLost, e)
		}
	}

	// RIP: where the fault entered and how far it travelled.
	rip := tr.RIP()
	row.InfectionPoint, row.InfectionDetail = rip.InfectionPoint, rip.InfectionDetail
	row.PropagationDepth, row.LastKnownWell = rip.PropagationDepth, rip.LastKnownWell

	if err != nil {
		row.Outcome = "order rejected: " + err.Error()
		if mode == "NONE" {
			// Without a fault the order must go through; anything else is a
			// setup problem (Ollama down, a service unreachable), not evidence.
			row.Error = row.Outcome
		} else {
			row.DetectedBy = "agent"
		}
		return row
	}

	row.OrderAccepted = true
	row.TrackingID = resp.GetOrder().GetShippingTrackingId()
	row.Checks = r.Agent.Check(ctx, req, resp, steps)
	for _, c := range row.Checks {
		if !c.Pass {
			row.FailedChecks = append(row.FailedChecks, c.Rule)
		}
	}
	// Ground truth, independent of the agent: is the cart really empty?
	if c, cerr := r.Cart.GetCart(ctx, &pb.GetCartRequest{UserId: TestUser}); cerr == nil && len(c.Items) > 0 {
		row.BoundaryAlert = "cart still has items after a successful order"
	}

	switch {
	case len(row.FailedChecks) > 0:
		row.DetectedBy = "checks"
		row.Outcome = "order accepted; correctness checks failed: " + strings.Join(row.FailedChecks, ", ")
	case row.InfectionPoint != "":
		row.Outcome = "order accepted despite the fault (not detected)"
	default:
		row.Outcome = "order accepted"
	}
	if mode == "NONE" && (row.InfectionPoint != "" || len(row.FailedChecks) > 0) {
		row.Error = "baseline order is not clean: " + row.Outcome
	}
	return row
}

// Save writes res to dir/ResultsFile.
func Save(dir string, res Results) error {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ResultsFile), b, 0o644)
}

// PrintReport writes a text report, in the style of the other agents'
// test_fault_injection.py.
func PrintReport(w io.Writer, res Results) {
	line := strings.Repeat("=", 72)
	fmt.Fprintf(w, "\n%s\n  CHECKOUTAGENT FAULT INJECTION — TEST REPORT (%s)\n%s\n", line, res.Backend, line)
	evidence, faults := 0, 0
	for _, r := range res.Results {
		label := "FAULT"
		if r.FaultMode == "NONE" {
			label = "PASS"
		} else {
			faults++
			if r.Error == "" && (len(r.StepsLost) > 0 || r.InfectionPoint != "") {
				evidence++
			}
		}
		fmt.Fprintf(w, "\n  [%s] %s  (%d ms)\n", label, r.FaultMode, r.ElapsedMS)
		if r.Error != "" {
			fmt.Fprintf(w, "  ERROR   : %s\n", r.Error)
		}
		fmt.Fprintf(w, "  Steps   : %s\n", orNone(strings.Join(r.StepsReached, " → ")))
		if len(r.StepsLost) > 0 {
			fmt.Fprintf(w, "  Lost    : %s\n", strings.Join(r.StepsLost, ", "))
		}
		fmt.Fprintf(w, "  LKW     : %s\n", orNone(r.LastKnownWell))
		fmt.Fprintf(w, "  Infects : %s\n", orNone(strings.TrimSpace(r.InfectionPoint+"  "+r.InfectionDetail)))
		fmt.Fprintf(w, "  Prop.   : %d\n", r.PropagationDepth)
		fmt.Fprintf(w, "  Caught  : %s\n", orNone(r.DetectedBy))
		fmt.Fprintf(w, "  Outcome : %s\n", r.Outcome)
	}
	fmt.Fprintf(w, "\n  Faults with evidence: %d / %d\n%s\n\n", evidence, faults, line)
}

// FilterModes keeps the known modes in in; empty means all of them.
func FilterModes(in []string) []string {
	if len(in) == 0 {
		return append([]string(nil), agent.Modes...)
	}
	ok := map[string]bool{}
	for _, m := range agent.Modes {
		ok[m] = true
	}
	var out []string
	for _, m := range in {
		if m = strings.ToUpper(strings.TrimSpace(m)); ok[m] {
			out = append(out, m)
		}
	}
	return out
}

func without(in []string, drop string) []string {
	var out []string
	for _, m := range in {
		if m != drop {
			out = append(out, m)
		}
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Live returns a Runner for the real agent and the real cart service.
func Live(a *agent.CheckoutAgent, cartAddr, currency string) (*Runner, error) {
	conn, err := grpc.NewClient(cartAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Runner{Agent: a, Cart: pb.NewCartServiceClient(conn), Currency: currency, Backend: "live"}, nil
}
