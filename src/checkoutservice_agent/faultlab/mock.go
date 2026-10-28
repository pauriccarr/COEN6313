package faultlab

// Mock backend: in-process fake downstream services and a fake model, so the
// fault suite runs in a second with no cluster and no Ollama (the Go version
// of mocking ProductCatalogGrpcClient in test_fault_injection.py).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"

	"checkoutservice-agent/agent"
	pb "checkoutservice-agent/genproto"
	"checkoutservice-agent/ollama"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// rates converts 1 USD into each currency the fakes know.
var rates = map[string]float64{"USD": 1, "EUR": 0.92, "GBP": 0.79, "CAD": 1.37, "JPY": 151}

var catalog = map[string]*pb.Money{
	"OLJCESPC7Z": {CurrencyCode: "USD", Units: 19, Nanos: 990000000},
	"66VCHSJNUP": {CurrencyCode: "USD", Units: 12, Nanos: 500000000},
}

// Fakes implements every downstream service the agent calls, and records
// what really happened (charges, shipments, emails) as ground truth.
type Fakes struct {
	pb.UnimplementedCartServiceServer
	pb.UnimplementedProductCatalogServiceServer
	pb.UnimplementedCurrencyServiceServer
	pb.UnimplementedShippingServiceServer
	pb.UnimplementedPaymentServiceServer
	pb.UnimplementedEmailServiceServer

	mu        sync.Mutex
	Carts     map[string][]*pb.CartItem
	Charges   []*pb.ChargeRequest
	Shipments []*pb.ShipOrderRequest
	Emails    int
}

func (f *Fakes) AddItem(_ context.Context, r *pb.AddItemRequest) (*pb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Carts[r.UserId] = append(f.Carts[r.UserId], r.Item)
	return &pb.Empty{}, nil
}

func (f *Fakes) GetCart(_ context.Context, r *pb.GetCartRequest) (*pb.Cart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &pb.Cart{UserId: r.UserId, Items: f.Carts[r.UserId]}, nil
}

func (f *Fakes) EmptyCart(_ context.Context, r *pb.EmptyCartRequest) (*pb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.Carts, r.UserId)
	return &pb.Empty{}, nil
}

func (f *Fakes) GetProduct(_ context.Context, r *pb.GetProductRequest) (*pb.Product, error) {
	price, ok := catalog[r.Id]
	if !ok {
		return nil, fmt.Errorf("no product %q", r.Id)
	}
	return &pb.Product{Id: r.Id, Name: "p-" + r.Id, PriceUsd: price}, nil
}

func (f *Fakes) Convert(_ context.Context, r *pb.CurrencyConversionRequest) (*pb.Money, error) {
	from, ok1 := rates[r.From.GetCurrencyCode()]
	to, ok2 := rates[r.ToCode]
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("unsupported currency %s -> %s", r.From.GetCurrencyCode(), r.ToCode)
	}
	n := float64(r.From.Units)*1e9 + float64(r.From.Nanos)
	n = n / from * to
	nanos := int64(n/1e7+0.5) * 1e7 // round to cents
	return &pb.Money{CurrencyCode: r.ToCode, Units: nanos / 1e9, Nanos: int32(nanos % 1e9)}, nil
}

func (f *Fakes) GetQuote(_ context.Context, r *pb.GetQuoteRequest) (*pb.GetQuoteResponse, error) {
	if len(r.Items) == 0 {
		return nil, fmt.Errorf("quote for no items")
	}
	return &pb.GetQuoteResponse{CostUsd: &pb.Money{CurrencyCode: "USD", Units: 8, Nanos: 990000000}}, nil
}

func (f *Fakes) ShipOrder(_ context.Context, r *pb.ShipOrderRequest) (*pb.ShipOrderResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Shipments = append(f.Shipments, r)
	return &pb.ShipOrderResponse{TrackingId: fmt.Sprintf("TRK-MOCK-%d", len(f.Shipments))}, nil
}

func (f *Fakes) Charge(_ context.Context, r *pb.ChargeRequest) (*pb.ChargeResponse, error) {
	if r.CreditCard.GetCreditCardNumber() == "" {
		return nil, fmt.Errorf("no card")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Charges = append(f.Charges, r)
	return &pb.ChargeResponse{TransactionId: fmt.Sprintf("tx-mock-%d", len(f.Charges))}, nil
}

func (f *Fakes) SendOrderConfirmation(context.Context, *pb.SendOrderConfirmationRequest) (*pb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Emails++
	return &pb.Empty{}, nil
}

// planLLM plays the model in planner mode: it always returns the standard plan.
func planLLM(w http.ResponseWriter, _ *http.Request) {
	plan, _ := json.Marshal(map[string]any{"plan": agent.ExpectedSteps})
	_ = json.NewEncoder(w).Encode(map[string]any{"done": true,
		"message": ollama.Message{Role: "assistant", Content: string(plan)}})
}

// Mock is a running mock backend.
type Mock struct {
	Fakes *Fakes
	Addr  string // gRPC address of every fake service
	LLM   string // URL of the fake model
	stop  func()
}

// StartMock starts the fake services and the fake model on localhost.
func StartMock() (*Mock, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &Fakes{Carts: map[string][]*pb.CartItem{}}
	s := grpc.NewServer()
	pb.RegisterCartServiceServer(s, f)
	pb.RegisterProductCatalogServiceServer(s, f)
	pb.RegisterCurrencyServiceServer(s, f)
	pb.RegisterShippingServiceServer(s, f)
	pb.RegisterPaymentServiceServer(s, f)
	pb.RegisterEmailServiceServer(s, f)
	go s.Serve(lis)
	llm := httptest.NewServer(http.HandlerFunc(planLLM))
	return &Mock{Fakes: f, Addr: lis.Addr().String(), LLM: llm.URL,
		stop: func() { llm.Close(); s.Stop() }}, nil
}

// Close stops the mock backend.
func (m *Mock) Close() { m.stop() }

// Runner returns a Runner for an agent (planner mode) wired to the mock.
// log may be nil.
func (m *Mock) Runner(log *logrus.Logger) (*Runner, error) {
	if log == nil {
		log = logrus.New()
		log.Out = io.Discard
	}
	a, err := agent.New(agent.Config{CartSvcAddr: m.Addr, ProductCatalogAddr: m.Addr, CurrencySvcAddr: m.Addr,
		ShippingSvcAddr: m.Addr, PaymentSvcAddr: m.Addr, EmailSvcAddr: m.Addr,
		OllamaAddr: m.LLM, OllamaModel: "mock", Mode: "planner"}, log)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(m.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Runner{Agent: a, Cart: pb.NewCartServiceClient(conn), Currency: "EUR", Backend: "mock"}, nil
}
