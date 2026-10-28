package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"checkoutservice-agent/agent"
	"checkoutservice-agent/database"
	pb "checkoutservice-agent/genproto"
	"checkoutservice-agent/metrics"
	"checkoutservice-agent/ui"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/v2/bson"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

var (
	listenPort = mustGetEnv("PORT", "5050")
	httpPort   = mustGetEnv("HTTP_PORT", "8080")
)

var log *logrus.Logger

func init() {
	log = logrus.New()
	log.Level = logrus.DebugLevel
	log.Formatter = &logrus.JSONFormatter{
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime:  "timestamp",
			logrus.FieldKeyLevel: "severity",
			logrus.FieldKeyMsg:   "message",
		},
		TimestampFormat: time.RFC3339Nano,
	}
	log.Out = os.Stdout
}

// checkoutServiceServer wraps the agent and implements the gRPC server interface.
type checkoutServiceServer struct {
	pb.UnimplementedCheckoutServiceServer
	agent *agent.CheckoutAgent
}

func (s *checkoutServiceServer) Check(_ context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func (s *checkoutServiceServer) Watch(_ *healthpb.HealthCheckRequest, _ healthpb.Health_WatchServer) error {
	return status.Errorf(codes.Unimplemented, "health check via Watch not implemented")
}

func (s *checkoutServiceServer) List(_ context.Context, _ *healthpb.HealthListRequest) (*healthpb.HealthListResponse, error) {
	return &healthpb.HealthListResponse{}, nil
}

func (s *checkoutServiceServer) PlaceOrder(ctx context.Context, req *pb.PlaceOrderRequest) (*pb.PlaceOrderResponse, error) {
	log.Infof("[PlaceOrder] user_id=%q user_currency=%q", req.UserId, req.UserCurrency)

	start := time.Now()

	resp, steps, err := s.agent.PlaceOrderTraced(ctx, req)
	metrics.PlaceOrderDuration.Observe(time.Since(start).Seconds())

	statusValue := "SUCCESS"
	errorMsg := ""

	if err != nil {
		statusValue = "FAILED"
		errorMsg = err.Error()
		metrics.OrderFailures.Inc()
		log.Errorf("[PlaceOrder] failed after %v: %v", time.Since(start), err)
	} else {
		metrics.OrdersPlaced.WithLabelValues(req.UserCurrency).Inc()
		log.Infof("[PlaceOrder] done in %v", time.Since(start))
		// Correctness: rule checks on the accepted order (metrics + log only).
		for _, c := range s.agent.CheckOrder(ctx, req, resp, steps) {
			if !c.Pass {
				log.Warnf("[correctness] order=%s rule %s failed: %s", resp.GetOrder().GetOrderId(), c.Rule, c.Detail)
			}
		}
	}

	orderID := ""
	if resp != nil && resp.Order != nil {
		orderID = resp.Order.OrderId
	}

	if database.OrdersCollection == nil {
		return resp, err
	}
	_, dbErr := database.OrdersCollection.InsertOne(
		ctx,
		bson.D{
			{Key: "order_id", Value: orderID},
			{Key: "user_id", Value: req.UserId},
			{Key: "user_currency", Value: req.UserCurrency},
			{Key: "status", Value: statusValue},
			{Key: "error", Value: errorMsg},
			{Key: "latency_ms", Value: time.Since(start).Milliseconds()},
			{Key: "order_response", Value: resp},
			{Key: "created_at", Value: time.Now()},
		},
	)

	if dbErr != nil {
		log.Errorf("failed to save checkout order to MongoDB: %v", dbErr)
	}

	return resp, err
}

func mustGetEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	// Downstream service addresses
	cfg := agent.Config{
		CartSvcAddr:        mustGetEnv("CART_SERVICE_ADDR", "cartservice:7070"),
		ProductCatalogAddr: mustGetEnv("PRODUCT_CATALOG_SERVICE_ADDR", "productcatalogservice:3550"),
		CurrencySvcAddr:    mustGetEnv("CURRENCY_SERVICE_ADDR", "currencyservice:7000"),
		ShippingSvcAddr:    mustGetEnv("SHIPPING_SERVICE_ADDR", "shippingservice:50051"),
		PaymentSvcAddr:     mustGetEnv("PAYMENT_SERVICE_ADDR", "paymentservice:50051"),
		EmailSvcAddr:       mustGetEnv("EMAIL_SERVICE_ADDR", "emailservice:5000"),
		OllamaAddr:         mustGetEnv("OLLAMA_ADDR", "http://ollama:11434"),
		OllamaModel:        mustGetEnv("OLLAMA_MODEL", "llama3.1"),
		FaultMode:          mustGetEnv("FAULT_MODE", "NONE"),
		Mode:               mustGetEnv("AGENT_MODE", "planner"),
	}

	// Order logging to MongoDB is optional: set MONGO_URI from a Secret to
	// enable it. Without it, or if it is unreachable, checkout still works.
	if mongoURI := os.Getenv("MONGO_URI"); mongoURI != "" {
		if err := database.ConnectMongo(mongoURI); err != nil {
			log.Errorf("MongoDB unavailable, order logging disabled: %v", err)
		} else {
			log.Infof("MongoDB connected successfully")
		}
	}

	checkoutAgent, err := agent.New(cfg, log)
	if err != nil {
		log.Fatalf("failed to create checkout agent: %v", err)
	}
	defer checkoutAgent.Close()

	uiSrv, err := ui.New(checkoutAgent, cfg.CartSvcAddr, log)
	if err != nil {
		log.Fatalf("failed to create fault UI: %v", err)
	}
	go func() {
		log.Infof("starting HTTP server on :%s (/health, /ui)", httpPort)
		if err := http.ListenAndServe(":"+httpPort, uiSrv.Handler()); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", listenPort))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	srv := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)

	svc := &checkoutServiceServer{agent: checkoutAgent}
	pb.RegisterCheckoutServiceServer(srv, svc)
	healthpb.RegisterHealthServer(srv, svc)
	reflection.Register(srv)

	log.Infof("starting gRPC server on :%s (agent mode=%s, ollama=%s, model=%s, fault=%s)",
		listenPort, cfg.Mode, cfg.OllamaAddr, cfg.OllamaModel, cfg.FaultMode)

	if err := srv.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
