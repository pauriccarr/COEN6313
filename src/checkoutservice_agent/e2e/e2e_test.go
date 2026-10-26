// End-to-end test with no cluster: a scripted fake Ollama and in-process fake
// downstream gRPC services. Run with: go test ./e2e -v
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"checkoutservice-agent/agent"
	pb "checkoutservice-agent/genproto"
	"checkoutservice-agent/ollama"
	"checkoutservice-agent/ui"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

// ---- fake downstream services -------------------------------------------------

type fakes struct {
	pb.UnimplementedCartServiceServer
	pb.UnimplementedProductCatalogServiceServer
	pb.UnimplementedCurrencyServiceServer
	pb.UnimplementedShippingServiceServer
	pb.UnimplementedPaymentServiceServer
	pb.UnimplementedEmailServiceServer

	mu      sync.Mutex
	carts   map[string][]*pb.CartItem
	charges []*pb.ChargeRequest
	emails  int
}

func (f *fakes) AddItem(_ context.Context, r *pb.AddItemRequest) (*pb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.carts[r.UserId] = append(f.carts[r.UserId], r.Item)
	return &pb.Empty{}, nil
}
func (f *fakes) GetCart(_ context.Context, r *pb.GetCartRequest) (*pb.Cart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &pb.Cart{UserId: r.UserId, Items: f.carts[r.UserId]}, nil
}
func (f *fakes) EmptyCart(_ context.Context, r *pb.EmptyCartRequest) (*pb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.carts, r.UserId)
	return &pb.Empty{}, nil
}
func (f *fakes) GetProduct(_ context.Context, r *pb.GetProductRequest) (*pb.Product, error) {
	if r.Id != "OLJCESPC7Z" && r.Id != "66VCHSJNUP" {
		return nil, fmt.Errorf("no product %q", r.Id)
	}
	return &pb.Product{Id: r.Id, Name: "p-" + r.Id, PriceUsd: &pb.Money{CurrencyCode: "USD", Units: 19, Nanos: 990000000}}, nil
}
func (f *fakes) Convert(_ context.Context, r *pb.CurrencyConversionRequest) (*pb.Money, error) {
	return &pb.Money{CurrencyCode: r.ToCode, Units: r.From.Units, Nanos: r.From.Nanos}, nil
}
func (f *fakes) GetQuote(_ context.Context, r *pb.GetQuoteRequest) (*pb.GetQuoteResponse, error) {
	if r.Address.StreetAddress != "1600 Amphitheatre Parkway" || len(r.Items) == 0 {
		return nil, fmt.Errorf("bad quote request: %v", r)
	}
	return &pb.GetQuoteResponse{CostUsd: &pb.Money{CurrencyCode: "USD", Units: 8, Nanos: 990000000}}, nil
}
func (f *fakes) ShipOrder(_ context.Context, r *pb.ShipOrderRequest) (*pb.ShipOrderResponse, error) {
	if r.Address.StreetAddress != "1600 Amphitheatre Parkway" {
		return nil, fmt.Errorf("bad address %q", r.Address.StreetAddress)
	}
	return &pb.ShipOrderResponse{TrackingId: "TRK-REAL-1"}, nil
}
func (f *fakes) Charge(_ context.Context, r *pb.ChargeRequest) (*pb.ChargeResponse, error) {
	if r.CreditCard.GetCreditCardNumber() != "4432-8015-6152-0454" {
		return nil, fmt.Errorf("bad card %q", r.CreditCard.GetCreditCardNumber())
	}
	f.mu.Lock()
	f.charges = append(f.charges, r)
	f.mu.Unlock()
	return &pb.ChargeResponse{TransactionId: "tx-1"}, nil
}
func (f *fakes) SendOrderConfirmation(context.Context, *pb.SendOrderConfirmationRequest) (*pb.Empty, error) {
	f.mu.Lock()
	f.emails++
	f.mu.Unlock()
	return &pb.Empty{}, nil
}

// ---- scripted fake Ollama -----------------------------------------------------

// fakeLLM plays a well-behaved model: it calls the next missing step, building
// arguments from earlier tool results, the way llama3 does (IDs and numbers
// sometimes as strings, arrays sometimes stringified).
func fakeLLM(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []ollama.Message `json:"messages"`
			Format   string           `json:"format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "4432") {
				t.Errorf("card number leaked into the prompt")
			}
		}
		if req.Format == "json" {
			_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "message": planReply(req.Messages)})
			return
		}
		done := map[string]string{}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				done[m.ToolName] = m.Content
			}
		}
		var cart struct {
			Items []struct {
				ProductID string `json:"product_id"`
				Quantity  int    `json:"quantity"`
			} `json:"items"`
		}
		_ = json.Unmarshal([]byte(done["get_cart"]), &cart)
		itemsJSON, _ := json.Marshal(cart.Items)

		call := func(name string, args map[string]any) ollama.Message {
			a, _ := json.Marshal(args)
			return ollama.Message{Role: "assistant", ToolCalls: []ollama.ToolCall{{Function: ollama.FunctionCall{Name: name, Arguments: a}}}}
		}
		addr := map[string]any{"street_address": "1600 Amphitheatre Parkway", "city": "Mountain View",
			"state": "CA", "country": "United States", "zip_code": "94043", "items": string(itemsJSON)}

		var msg ollama.Message
		switch {
		case done["get_cart"] == "":
			msg = call("get_cart", map[string]any{"user_id": "faultlab-checkout"})
		case done["get_product"] == "":
			var tcs []ollama.ToolCall
			for _, it := range cart.Items {
				a, _ := json.Marshal(map[string]any{"product_id": it.ProductID})
				tcs = append(tcs, ollama.ToolCall{Function: ollama.FunctionCall{Name: "get_product", Arguments: a}})
			}
			msg = ollama.Message{Role: "assistant", ToolCalls: tcs}
		case done["convert_currency"] == "":
			msg = call("convert_currency", map[string]any{"units": "19", "nanos": 990000000, "from_currency": "USD", "to_currency": "USD"})
		case done["quote_shipping"] == "":
			msg = call("quote_shipping", addr)
		case done["charge_card"] == "":
			msg = call("charge_card", map[string]any{"amount_units": 68, "amount_nanos": 960000000, "currency_code": "USD"})
		case done["ship_order"] == "":
			msg = call("ship_order", addr)
		case done["empty_cart"] == "":
			msg = call("empty_cart", map[string]any{"user_id": "faultlab-checkout"})
		case done["send_order_confirmation"] == "":
			msg = call("send_order_confirmation", map[string]any{"email": "faultlab@example.com", "order_id": "3fa85f64",
				"shipping_tracking_id": "TRK-REAL-1", "shipping_cost_units": 8, "shipping_cost_nanos": 990000000,
				"shipping_currency": "USD", "items": "[]"})
		default:
			// Final answer with a hallucinated tracking ID: verification must replace it.
			msg = ollama.Message{Role: "assistant", Content: "Sure! " +
				`{"order_id":"x","shipping_tracking_id":"LLM-MADE-UP","shipping_cost":{"currency_code":"USD","units":8,"nanos":990000000},` +
				`"items":[{"item":{"product_id":"OLJCESPC7Z","quantity":1},"cost":{"currency_code":"USD","units":19,"nanos":990000000}}]}`}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": msg, "done": true})
	}
}

// planReply plays a small model in planner mode: its first plan forgets
// empty_cart; told what is wrong, it sends the full plan.
func planReply(msgs []ollama.Message) ollama.Message {
	plan := `{"plan":["get_cart","get_product","convert_currency","quote_shipping","charge_card","ship_order","send_order_confirmation"]}`
	if strings.Contains(msgs[len(msgs)-1].Content, "not valid") {
		plan = `{"plan":["get_cart","get_product","quote_shipping","convert_currency","charge_card","ship_order","empty_cart","send_order_confirmation"]}`
	}
	return ollama.Message{Role: "assistant", Content: plan}
}

// lazyLLM answers right after get_cart, the way llama3.2:1b does, until it is
// told the order is not complete; then it behaves like fakeLLM.
func lazyLLM(t *testing.T) http.HandlerFunc {
	good := fakeLLM(t)
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []ollama.Message `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		nudged, gotCart := false, false
		for _, m := range req.Messages {
			nudged = nudged || (m.Role == "user" && strings.Contains(m.Content, "not complete"))
			gotCart = gotCart || (m.Role == "tool" && m.ToolName == "get_cart")
		}
		if gotCart && !nudged {
			_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "message": ollama.Message{
				Role: "assistant", Content: `{"order_id":"x","shipping_tracking_id":"LLM-MADE-UP"}`}})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		good(w, r)
	}
}

func setup(t *testing.T) (*agent.CheckoutAgent, *fakes, string) {
	return setupWith(t, fakeLLM(t), "planner")
}

func setupWith(t *testing.T, model http.HandlerFunc, mode string) (*agent.CheckoutAgent, *fakes, string) {
	f := &fakes{carts: map[string][]*pb.CartItem{}}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	pb.RegisterCartServiceServer(s, f)
	pb.RegisterProductCatalogServiceServer(s, f)
	pb.RegisterCurrencyServiceServer(s, f)
	pb.RegisterShippingServiceServer(s, f)
	pb.RegisterPaymentServiceServer(s, f)
	pb.RegisterEmailServiceServer(s, f)
	go s.Serve(lis)
	t.Cleanup(s.Stop)

	llm := httptest.NewServer(model)
	t.Cleanup(llm.Close)

	addr := lis.Addr().String()
	log := logrus.New()
	log.Out = io.Discard
	a, err := agent.New(agent.Config{CartSvcAddr: addr, ProductCatalogAddr: addr, CurrencySvcAddr: addr,
		ShippingSvcAddr: addr, PaymentSvcAddr: addr, EmailSvcAddr: addr, OllamaAddr: llm.URL, OllamaModel: "fake", Mode: mode}, log)
	if err != nil {
		t.Fatal(err)
	}
	return a, f, addr
}

func TestPlaceOrderHappyPath(t *testing.T) {
	for _, mode := range []string{"planner", "react"} {
		t.Run(mode, func(t *testing.T) { testHappyPath(t, fakeLLM(t), mode) })
	}
}

// A model that never returns a usable plan: the standard order is used.
func TestPlannerFallsBackToStandardPlan(t *testing.T) {
	garbage := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true,
			"message": ollama.Message{Role: "assistant", Content: "I will now process the checkout for you!"}})
	}
	testHappyPath(t, garbage, "planner")
}

func testHappyPath(t *testing.T, model http.HandlerFunc, mode string) {
	t.Setenv("RESULTS_DIR", t.TempDir())
	a, f, _ := setupWith(t, model, mode)
	f.carts["faultlab-checkout"] = []*pb.CartItem{{ProductId: "OLJCESPC7Z", Quantity: 1}, {ProductId: "66VCHSJNUP", Quantity: 2}}
	resp, steps, err := a.PlaceOrderTraced(context.Background(), &pb.PlaceOrderRequest{
		UserId: "faultlab-checkout", UserCurrency: "USD", Email: "faultlab@example.com",
		Address: &pb.Address{StreetAddress: "1600 Amphitheatre Parkway", City: "Mountain View", State: "CA",
			Country: "United States", ZipCode: 94043},
		CreditCard: &pb.CreditCardInfo{CreditCardNumber: "4432-8015-6152-0454", CreditCardCvv: 672,
			CreditCardExpirationYear: 2039, CreditCardExpirationMonth: 1},
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v\nsteps: %+v", err, steps)
	}
	for _, s := range steps {
		if !s.OK {
			t.Errorf("step %s failed: %s", s.Tool, s.Error)
		}
	}
	if got := resp.Order.ShippingTrackingId; got != "TRK-REAL-1" {
		t.Errorf("tracking ID = %q, want the one from ship_order", got)
	}
	if len(f.charges) != 1 || len(f.carts["faultlab-checkout"]) != 0 || f.emails != 1 {
		t.Fatalf("charges=%d cart=%v emails=%d", len(f.charges), f.carts["faultlab-checkout"], f.emails)
	}
	// 3 x 19.99 + 8.99 shipping
	if m := f.charges[0].Amount; m.Units != 68 || m.Nanos != 960000000 || m.CurrencyCode != "USD" {
		t.Errorf("charged %v, want USD 68.96", m)
	}
}

func TestPlaceOrderNudgesEarlyAnswer(t *testing.T) {
	t.Setenv("RESULTS_DIR", t.TempDir())
	a, f, _ := setupWith(t, lazyLLM(t), "react")
	f.carts["faultlab-checkout"] = []*pb.CartItem{{ProductId: "OLJCESPC7Z", Quantity: 1}}
	resp, steps, err := a.PlaceOrderTraced(context.Background(), &pb.PlaceOrderRequest{
		UserId: "faultlab-checkout", UserCurrency: "USD", Email: "faultlab@example.com",
		Address: &pb.Address{StreetAddress: "1600 Amphitheatre Parkway", City: "Mountain View", State: "CA",
			Country: "United States", ZipCode: 94043},
		CreditCard: &pb.CreditCardInfo{CreditCardNumber: "4432-8015-6152-0454", CreditCardCvv: 672,
			CreditCardExpirationYear: 2039, CreditCardExpirationMonth: 1},
	})
	if err != nil {
		t.Fatalf("PlaceOrder after an early answer: %v\nsteps: %+v", err, steps)
	}
	if got := resp.Order.ShippingTrackingId; got != "TRK-REAL-1" {
		t.Errorf("tracking ID = %q, want the one from ship_order", got)
	}
	if len(f.charges) != 1 {
		t.Errorf("charges = %d, want 1", len(f.charges))
	}
}

func TestPlaceOrderRejectsMissingCard(t *testing.T) {
	a, _, _ := setup(t)
	if _, err := a.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{UserId: "u"}); err == nil {
		t.Fatal("expected InvalidArgument for a request with no address or card")
	}
}

func TestFaultRunnerEndpoints(t *testing.T) {
	t.Setenv("RESULTS_DIR", t.TempDir())
	a, _, addr := setup(t)
	log := logrus.New()
	log.Out = io.Discard
	srv, err := ui.New(a, addr, log)
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(srv.Handler())
	defer h.Close()

	for _, p := range []string{"/health", "/ui", "/ui/modes", "/ui/status", "/ui/results"} {
		r, err := http.Get(h.URL + p)
		if err != nil || r.StatusCode != 200 {
			t.Fatalf("GET %s: %v %v", p, err, r.Status)
		}
	}

	r, _ := http.Post(h.URL+"/ui/run", "application/json", bytes.NewBufferString(`{}`))
	r.Body.Close()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var st map[string]any
		r, _ := http.Get(h.URL + "/ui/status")
		_ = json.NewDecoder(r.Body).Decode(&st)
		r.Body.Close()
		if st["running"] == false {
			if st["returncode"] != float64(0) {
				t.Fatalf("run failed: %v", st["log"])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not finish")
		}
		time.Sleep(100 * time.Millisecond)
	}

	var res struct {
		Results []ui.Row `json:"results"`
	}
	r, _ = http.Get(h.URL + "/ui/results")
	_ = json.NewDecoder(r.Body).Decode(&res)
	r.Body.Close()
	if len(res.Results) != len(agent.Modes) {
		t.Fatalf("got %d rows, want %d", len(res.Results), len(agent.Modes))
	}
	for _, row := range res.Results {
		t.Logf("%-20s accepted=%-5v infection=%-24q lost=%v  %s", row.FaultMode, row.OrderAccepted,
			row.InfectionPoint, row.StepsLost, row.Outcome)
		if row.FaultMode == "NONE" {
			if row.Error != "" || row.InfectionPoint != "" || len(row.StepsLost) != 0 {
				t.Errorf("baseline not clean: %+v", row)
			}
		} else if row.InfectionPoint == "" {
			t.Errorf("%s left no evidence", row.FaultMode)
		}
	}
}
