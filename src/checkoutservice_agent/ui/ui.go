// Package ui serves the HTTP side of the agent on :8080: /health and the
// fault-injection endpoints the FaultLab dashboard drives
// (/ui, /ui/modes, /ui/run, /ui/status, /ui/results).
package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"checkoutservice-agent/agent"
	pb "checkoutservice-agent/genproto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const resultsFile = "checkoutagent_fault_results.json"

// Test order used by every run: a dedicated user, so real carts are untouched.
const testUser = "faultlab-checkout"

var testItems = []*pb.CartItem{
	{ProductId: "OLJCESPC7Z", Quantity: 1},
	{ProductId: "66VCHSJNUP", Quantity: 2},
}

// Row is one fault mode's result, in the shape the dashboard renders.
type Row struct {
	FaultMode        string       `json:"fault_mode"`
	StepsReached     []string     `json:"steps_reached"`
	StepsLost        []string     `json:"steps_lost"`
	InfectionPoint   string       `json:"infection_point,omitempty"`
	PropagationDepth int          `json:"propagation_depth"`
	BoundaryAlert    string       `json:"boundary_alert_point,omitempty"`
	OrderAccepted    bool         `json:"order_accepted"`
	TrackingID       string       `json:"tracking_id,omitempty"`
	Outcome          string       `json:"outcome"`
	Trace            []agent.Step `json:"trace"`
	LatencyMS        int64        `json:"latency_ms"`
	Error            string       `json:"error,omitempty"`
}

type Server struct {
	agent *agent.CheckoutAgent
	cart  pb.CartServiceClient
	log   *logrus.Logger
	dir   string

	mu      sync.Mutex
	status  map[string]any
	results map[string]any
	logBuf  strings.Builder
}

func New(a *agent.CheckoutAgent, cartAddr string, log *logrus.Logger) (*Server, error) {
	conn, err := grpc.NewClient(cartAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	dir := os.Getenv("RESULTS_DIR")
	if dir == "" {
		dir = "/results"
	}
	s := &Server{agent: a, cart: pb.NewCartServiceClient(conn), log: log, dir: dir,
		status: map[string]any{"running": false, "log": ""}}
	// Results survive a restart if /results is a volume.
	if b, err := os.ReadFile(filepath.Join(dir, resultsFile)); err == nil {
		_ = json.Unmarshal(b, &s.results)
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ui/modes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"modes": agent.Modes})
	})
	mux.HandleFunc("POST /ui/run", s.run)
	mux.HandleFunc("GET /ui/status", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := map[string]any{}
		for k, v := range s.status {
			out[k] = v
		}
		out["log"] = s.logBuf.String()
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /ui/results", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.results == nil {
			writeJSON(w, map[string]any{"results": []any{}})
			return
		}
		writeJSON(w, s.results)
	})
	mux.HandleFunc("GET /ui", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	return mux
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Modes []string `json:"modes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	modes := filterModes(body.Modes)

	s.mu.Lock()
	if s.status["running"] == true {
		s.mu.Unlock()
		writeJSON(w, map[string]string{"status": "busy"})
		return
	}
	s.status = map[string]any{"running": true, "started_at": now(), "modes": modes}
	s.logBuf.Reset()
	s.mu.Unlock()

	go s.runAll(modes)
	writeJSON(w, map[string]any{"status": "started", "modes": modes})
}

func (s *Server) logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(&s.logBuf, format+"\n", args...)
}

func (s *Server) runAll(modes []string) {
	rc := 0
	defer func() {
		if p := recover(); p != nil {
			s.logf("PANIC: %v", p)
			rc = 2
		}
		s.mu.Lock()
		s.status["running"] = false
		s.status["finished_at"] = now()
		s.status["returncode"] = rc
		s.mu.Unlock()
	}()

	// NONE runs first so the other modes can be compared against it.
	if len(modes) == 0 || modes[0] != "NONE" {
		modes = append([]string{"NONE"}, without(modes, "NONE")...)
	}
	var rows []Row
	var baseline *Row
	for _, m := range modes {
		s.logf("== %s", m)
		row := s.runOne(m)
		if m == "NONE" {
			baseline = &row
		} else {
			score(&row, baseline)
		}
		if row.Error != "" {
			rc = 1
		}
		s.logf("   accepted=%v reached=%v lost=%v infection=%q %s",
			row.OrderAccepted, row.StepsReached, row.StepsLost, row.InfectionPoint, row.Outcome)
		rows = append(rows, row)
	}

	res := map[string]any{"agent": "checkoutservice", "generated_at": now(), "results": rows}
	s.mu.Lock()
	s.results = res
	s.mu.Unlock()
	if b, err := json.MarshalIndent(res, "", "  "); err == nil {
		if err := os.WriteFile(filepath.Join(s.dir, resultsFile), b, 0o644); err != nil {
			s.logf("could not save results to %s (kept in memory): %v", s.dir, err)
		}
	}
}

func (s *Server) runOne(mode string) Row {
	row := Row{FaultMode: mode}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Fresh cart for the test user.
	_, _ = s.cart.EmptyCart(ctx, &pb.EmptyCartRequest{UserId: testUser})
	for _, it := range testItems {
		if _, err := s.cart.AddItem(ctx, &pb.AddItemRequest{UserId: testUser, Item: it}); err != nil {
			row.Error = "seeding cart: " + err.Error()
			return row
		}
	}

	start := time.Now()
	resp, steps, err := s.agent.PlaceOrderTraced(agent.WithFaultMode(ctx, mode), &pb.PlaceOrderRequest{
		UserId:       testUser,
		UserCurrency: "USD",
		Email:        "faultlab@example.com",
		Address: &pb.Address{StreetAddress: "1600 Amphitheatre Parkway", City: "Mountain View",
			State: "CA", Country: "United States", ZipCode: 94043},
		CreditCard: &pb.CreditCardInfo{CreditCardNumber: "4432-8015-6152-0454",
			CreditCardCvv: 672, CreditCardExpirationYear: 2039, CreditCardExpirationMonth: 1},
	})
	row.LatencyMS = time.Since(start).Milliseconds()
	row.Trace = steps

	// Did the cart really get emptied? (Ground truth, independent of the agent.)
	if c, cerr := s.cart.GetCart(ctx, &pb.GetCartRequest{UserId: testUser}); cerr == nil && len(c.Items) > 0 && err == nil {
		row.BoundaryAlert = "cart still has items after a successful order"
	}

	reached := map[string]bool{}
	for _, st := range steps {
		if st.OK && !st.Faked && !reached[st.Tool] {
			reached[st.Tool] = true
			row.StepsReached = append(row.StepsReached, st.Tool)
		}
	}
	for _, e := range agent.ExpectedSteps {
		if !reached[e] {
			row.StepsLost = append(row.StepsLost, e)
		}
	}
	if row.StepsReached == nil {
		row.StepsReached = []string{}
	}
	if row.StepsLost == nil {
		row.StepsLost = []string{}
	}

	if err != nil {
		row.Outcome = "order rejected: " + err.Error()
	} else {
		row.OrderAccepted = true
		row.TrackingID = resp.GetOrder().GetShippingTrackingId()
		row.Outcome = "order accepted"
	}
	if mode == "NONE" && err != nil {
		// Without a fault the order must go through; anything else is a setup
		// problem (Ollama down, a service unreachable), not fault evidence.
		row.Error = row.Outcome
	}
	return row
}

// Steps that must happen exactly once per order; get_product and
// convert_currency legitimately repeat per item.
var once = map[string]bool{"get_cart": true, "charge_card": true, "ship_order": true,
	"empty_cart": true, "send_order_confirmation": true}

// score sets infection_point and propagation_depth for a fault run by comparing
// its trace with the step list (and the NONE baseline).
func score(row *Row, baseline *Row) {
	if row.Error != "" {
		return
	}
	count := map[string]int{}
	for _, st := range row.Trace {
		if st.OK && !st.Faked {
			count[st.Tool]++
		}
	}
	base := map[string]int{}
	if baseline != nil {
		for _, st := range baseline.Trace {
			if st.OK && !st.Faked {
				base[st.Tool]++
			}
		}
	}
	for i, e := range agent.ExpectedSteps {
		bad := count[e] == 0 || (baseline != nil && once[e] && count[e] > base[e])
		if bad {
			row.InfectionPoint = e
			row.PropagationDepth = len(agent.ExpectedSteps) - i
			break
		}
	}
	// The order went through every step, but the answer the caller got does
	// not match what ship_order returned (e.g. FM_3_2 with a hallucinated ID).
	if row.InfectionPoint == "" && row.OrderAccepted && row.TrackingID != realTracking(row.Trace) {
		row.InfectionPoint = "final_answer"
		row.PropagationDepth = 1
	}
	if row.InfectionPoint != "" && row.OrderAccepted {
		row.Outcome += " despite the fault (not detected)"
	} else if row.InfectionPoint != "" {
		row.Outcome += " (fault detected)"
	}
}

func realTracking(steps []agent.Step) string {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Tool == "ship_order" && steps[i].OK && !steps[i].Faked {
			var r struct {
				TrackingID string `json:"tracking_id"`
			}
			_ = json.Unmarshal([]byte(steps[i].Result), &r)
			return r.TrackingID
		}
	}
	return ""
}

func filterModes(in []string) []string {
	if len(in) == 0 {
		return append([]string(nil), agent.Modes...)
	}
	ok := map[string]bool{}
	for _, m := range agent.Modes {
		ok[m] = true
	}
	var out []string
	for _, m := range in {
		if ok[m] {
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

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

const page = `<!doctype html><html><head><meta charset="utf-8"><title>Checkout agent faults</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>body{font:14px system-ui;margin:16px;max-width:900px}pre{background:#f4f4f4;padding:8px;white-space:pre-wrap}
table{border-collapse:collapse}td,th{border:1px solid #ccc;padding:4px 8px;text-align:left}</style></head><body>
<h1>Checkout agent: fault injection</h1>
<div id="modes"></div><button id="run">Run selected</button> <span id="st"></span>
<pre id="log"></pre><div id="res"></div>
<script>
const $=s=>document.querySelector(s);
const esc=s=>String(s??"").replace(/[&<>"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c]));
fetch("ui/modes").then(r=>r.json()).then(d=>{$("#modes").innerHTML=d.modes.map(m=>
 '<label><input type=checkbox value="'+m+'" checked> '+m+'</label> ').join("")});
$("#run").onclick=()=>fetch("ui/run",{method:"POST",headers:{"Content-Type":"application/json"},
 body:JSON.stringify({modes:[...document.querySelectorAll("#modes input:checked")].map(i=>i.value)})}).then(poll);
async function poll(){const s=await (await fetch("ui/status")).json();
 $("#st").textContent=s.running?"Running…":(s.finished_at?"Finished (exit "+s.returncode+")":"Idle");
 $("#log").textContent=s.log||"";const r=await (await fetch("ui/results")).json();
 $("#res").innerHTML="<table><tr><th>Mode</th><th>Reached</th><th>Lost</th><th>Infection</th><th>Outcome</th></tr>"+
 (r.results||[]).map(x=>"<tr><td>"+esc(x.fault_mode)+"</td><td>"+esc((x.steps_reached||[]).join(" → "))+"</td><td>"+
 esc((x.steps_lost||[]).join(", "))+"</td><td>"+esc(x.infection_point||"none")+"</td><td>"+esc(x.error||x.outcome)+"</td></tr>").join("")+"</table>";
 if(s.running)setTimeout(poll,3000)}
poll();
</script></body></html>`
