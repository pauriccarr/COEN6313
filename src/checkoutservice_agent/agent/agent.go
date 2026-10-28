// Package agent implements the checkout agent.
//
// In planner mode (the default, planner.go) one LLM call chooses the plan and
// code runs it. In react mode (AGENT_MODE=react) it receives a PlaceOrder
// request, constructs a system+user prompt describing the checkout task, then
// runs a ReAct loop:
//
//  1. Send messages + tool definitions to Ollama.
//  2. If the model emits tool_calls, dispatch them to the real gRPC services.
//  3. Feed results back as tool messages.
//  4. Repeat until the model emits a final text message containing the order
//     summary JSON (or returns an error).
//
// The final answer is then verified against what the tools actually did
// (verifyOrder), so a model that skips payment or invents a tracking ID cannot
// report a successful order. Fault modes (faults.go) break this on purpose.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	pb "checkoutservice-agent/genproto"
	"checkoutservice-agent/ollama"
	"checkoutservice-agent/tools"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxIterations = 20

// maxNudges is how many times an early final answer is sent back to the model.
const maxNudges = 3

// Config holds all addresses the agent needs.
type Config struct {
	CartSvcAddr        string
	ProductCatalogAddr string
	CurrencySvcAddr    string
	ShippingSvcAddr    string
	PaymentSvcAddr     string
	EmailSvcAddr       string
	OllamaAddr         string
	OllamaModel        string
	// FaultMode is the default fault for live traffic (FAULT_MODE env).
	FaultMode string
	// Mode is "planner" (one LLM call picks the plan, code runs it; default)
	// or "react" (the LLM picks every tool call step by step). AGENT_MODE env.
	Mode string
}

// CheckoutAgent orchestrates the checkout flow via an LLM tool-use loop.
type CheckoutAgent struct {
	cfg   Config
	llm   *ollama.Client
	tools map[string]tools.Tool
	defs  []ollama.ToolDefinition
	log   *logrus.Logger
}

// Step is one tool call the agent made (or was faked by a fault mode).
type Step struct {
	Tool  string `json:"tool"`
	OK    bool   `json:"ok"`
	Faked bool   `json:"faked,omitempty"`
	// Tampered is set when a fault mode changed the call's arguments or result.
	Tampered bool   `json:"tampered,omitempty"`
	Error    string `json:"error,omitempty"`
	Args     string `json:"-"`
	Result   string `json:"-"`
	// Raw is what the service really returned, when a fault mode changed it.
	Raw string `json:"-"`
}

// New creates a CheckoutAgent, wiring up all downstream gRPC tool clients.
func New(cfg Config, log *logrus.Logger) (*CheckoutAgent, error) {
	a := &CheckoutAgent{
		cfg:   cfg,
		llm:   ollama.New(cfg.OllamaAddr, cfg.OllamaModel),
		tools: make(map[string]tools.Tool),
		log:   log,
	}

	register := func(t tools.Tool, err error) error {
		if err != nil {
			return err
		}
		a.tools[t.Name()] = t
		a.defs = append(a.defs, t.Definition())
		return nil
	}

	// Register all tools (one per downstream gRPC service / operation).
	if err := register(tools.NewGetCartTool(cfg.CartSvcAddr)); err != nil {
		return nil, fmt.Errorf("get_cart tool: %w", err)
	}
	if err := register(tools.NewEmptyCartTool(cfg.CartSvcAddr)); err != nil {
		return nil, fmt.Errorf("empty_cart tool: %w", err)
	}
	if err := register(tools.NewGetProductTool(cfg.ProductCatalogAddr)); err != nil {
		return nil, fmt.Errorf("get_product tool: %w", err)
	}
	if err := register(tools.NewConvertCurrencyTool(cfg.CurrencySvcAddr)); err != nil {
		return nil, fmt.Errorf("convert_currency tool: %w", err)
	}
	if err := register(tools.NewQuoteShippingTool(cfg.ShippingSvcAddr)); err != nil {
		return nil, fmt.Errorf("quote_shipping tool: %w", err)
	}
	if err := register(tools.NewShipOrderTool(cfg.ShippingSvcAddr)); err != nil {
		return nil, fmt.Errorf("ship_order tool: %w", err)
	}
	if err := register(tools.NewChargeCardTool(cfg.PaymentSvcAddr)); err != nil {
		return nil, fmt.Errorf("charge_card tool: %w", err)
	}
	if err := register(tools.NewSendConfirmationTool(cfg.EmailSvcAddr)); err != nil {
		return nil, fmt.Errorf("send_order_confirmation tool: %w", err)
	}

	return a, nil
}

// Close is a no-op placeholder; gRPC connections are closed per-request
// (stateless dial in tools). Extend here if you want pooled connections.
func (a *CheckoutAgent) Close() {}

const systemPrompt = `You are a checkout agent for an e-commerce platform.

Complete the checkout by calling the tools, in this order:
1. get_cart for the user.
2. get_product for every product in the cart.
3. convert_currency for every product price (and the shipping quote) into the user's currency.
4. quote_shipping for the cart items and the address.
5. charge_card for the total: sum of (converted price x quantity) plus converted shipping, in the user's currency.
6. ship_order for the cart items and the address.
7. empty_cart for the user.
8. send_order_confirmation to the user's email.

Never invent a result: only use values returned by the tools.
If a tool returns an error, stop and report it.

When every step is done, reply with ONE JSON object and nothing else
(no markdown, no explanation):
{
  "order_id": "<orderID>",
  "shipping_tracking_id": "<trackingID>",
  "shipping_cost": {"currency_code":"...","units":0,"nanos":0},
  "items": [
    {
      "item":{"product_id":"...","quantity":0},
      "cost":{"currency_code":"...","units":0,"nanos":0}
    }
  ]
}`

// PlaceOrder is the entry point called by the gRPC server.
func (a *CheckoutAgent) PlaceOrder(ctx context.Context, req *pb.PlaceOrderRequest) (*pb.PlaceOrderResponse, error) {
	resp, _, err := a.PlaceOrderTraced(ctx, req)
	return resp, err
}

// PlaceOrderTraced runs the checkout (planner or ReAct, see Config.Mode) and
// also returns every tool step, for the fault-injection runner. LKW
// checkpoints and the RIP record go to the Tracker in ctx (see WithTracker).
func (a *CheckoutAgent) PlaceOrderTraced(ctx context.Context, req *pb.PlaceOrderRequest) (*pb.PlaceOrderResponse, []Step, error) {
	if req.GetAddress() == nil || req.GetCreditCard() == nil {
		return nil, nil, status.Error(codes.InvalidArgument, "address and credit_card are required")
	}
	mode := a.faultMode(ctx)
	orderID := uuid.New().String()
	a.log.Infof("[agent] starting order=%s user=%s currency=%s fault=%s", orderID, req.UserId, req.UserCurrency, mode)

	tr := trackerFrom(ctx)
	if tr == nil {
		ctx, tr = WithTracker(ctx)
	}
	tr.checkpoint("TASK_START", "order "+orderID)
	ctx = tools.WithCreditCard(ctx, req.CreditCard)

	var resp *pb.PlaceOrderResponse
	var steps []Step
	var err error
	if a.cfg.Mode != "react" {
		resp, steps, err = a.placeOrderPlanned(ctx, req, orderID, mode)
	} else {
		resp, steps, err = a.placeOrderReact(ctx, req, orderID, mode)
	}
	if err == nil {
		tr.checkpoint("FINAL_ANSWER", "order "+orderID+" accepted")
	}
	if rip := tr.RIP(); rip.InfectionPoint != "" {
		a.log.Warnf("[RIP] order=%s fault=%s infection=%s depth=%d last_known_well=%s: %s",
			orderID, mode, rip.InfectionPoint, rip.PropagationDepth, rip.LastKnownWell, rip.InfectionDetail)
	}
	return resp, steps, err
}

// placeOrderReact is the ReAct loop: the model picks every tool call.
func (a *CheckoutAgent) placeOrderReact(ctx context.Context, req *pb.PlaceOrderRequest, orderID, mode string) (*pb.PlaceOrderResponse, []Step, error) {
	tr := trackerFrom(ctx)

	// The card is deliberately not in the prompt; charge_card reads it from ctx.
	userMsg := fmt.Sprintf(`Process checkout for:
- order_id: %s
- user_id: %s
- user_currency: %s
- email: %s
- address_street: %s
- address_city: %s
- address_state: %s
- address_country: %s
- address_zip: %d`,
		orderID,
		req.UserId,
		req.UserCurrency,
		req.Email,
		req.Address.StreetAddress,
		req.Address.City,
		req.Address.State,
		req.Address.Country,
		req.Address.ZipCode,
	)
	prompt := systemPrompt
	if mode == "FM_1_2" {
		prompt += "\n\nImportant: ship the order (ship_order) first, before you charge the card."
	}
	messages := []ollama.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: userMsg},
	}
	var steps []Step
	nudges := 0

	// ReAct loop
	for i := 0; i < maxIterations; i++ {
		a.log.Debugf("[agent] iteration %d  calling LLM", i+1)

		reply, err := a.llm.Chat(ctx, messages, a.defs)
		if err != nil {
			return nil, steps, status.Errorf(codes.Unavailable, "LLM error: %v", err)
		}

		// Append assistant message to history
		messages = append(messages, *reply)

		// No tool calls → final answer
		if len(reply.ToolCalls) == 0 {
			// Small models often answer before doing every step. Point out
			// what is missing and let the model continue, a few times at most.
			if mode != "FM_3_2" {
				if missing := missingSteps(steps); len(missing) > 0 && nudges < maxNudges {
					nudges++
					a.log.Warnf("[agent] order=%s answered early, still missing %s (nudge %d/%d)",
						orderID, strings.Join(missing, ", "), nudges, maxNudges)
					messages = append(messages, ollama.Message{
						Role: "user",
						Content: fmt.Sprintf("The order is not complete. You have not successfully called: %s. "+
							"Call the next missing tool now. Do not answer until every step is done.",
							strings.Join(missing, ", ")),
					})
					continue
				}
			}
			a.log.Infof("[agent] final answer received for order=%s", orderID)
			resp, err := parseOrderResponse(reply.Content, req)
			if err != nil {
				return nil, steps, err
			}
			if err := a.verify(ctx, mode, resp, orderID, steps); err != nil {
				return nil, steps, err
			}
			return resp, steps, nil
		}

		// Dispatch every tool call and collect results
		for _, tc := range reply.ToolCalls {
			toolName := tc.Function.Name
			a.log.Infof("[agent] tool_call name=%s args=%s", toolName, tc.Function.Arguments)

			result := a.dispatch(ctx, mode, toolName, parseArguments(tc.Function.Arguments), &steps)
			a.log.Debugf("[agent] tool %s result: %s", toolName, result)
			messages = append(messages, ollama.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				ToolName:   toolName,
				Content:    result,
			})
		}

		// FM_3_1: the agent declares the order done after its first round of
		// tool calls, without the remaining steps or any verification.
		if mode == "FM_3_1" {
			a.log.Warnf("[agent] FM_3_1: terminating early for order=%s", orderID)
			tr.infect("FINAL_ANSWER", "reported success after the first round of tool calls")
			return &pb.PlaceOrderResponse{Order: &pb.OrderResult{
				OrderId:         orderID,
				ShippingAddress: req.Address,
				ShippingCost:    &pb.Money{CurrencyCode: req.UserCurrency},
			}}, steps, nil
		}
	}

	return nil, steps, status.Errorf(codes.Internal, "agent exceeded max iterations (%d) without completing checkout", maxIterations)
}

// dispatch runs one tool call, applying the fault mode, records it in steps and
// returns the content to feed back to the model.
func (a *CheckoutAgent) dispatch(ctx context.Context, mode, toolName, args string, steps *[]Step) string {
	tool, ok := a.tools[toolName]
	if !ok {
		errMsg := fmt.Sprintf("unknown tool: %s", toolName)
		a.log.Warnf("[agent] %s", errMsg)
		*steps = append(*steps, Step{Tool: toolName, Error: errMsg})
		return jsonError(errMsg)
	}

	tr := trackerFrom(ctx)
	checkpoint := toolCheckpoint[toolName]

	if fake, ok := fakedResult(mode, toolName, args); ok {
		a.log.Warnf("[agent] %s: faked %s", mode, toolName)
		tr.infect(checkpoint, toolName+" was not called; its result was made up")
		*steps = append(*steps, Step{Tool: toolName, OK: true, Faked: true, Args: args, Result: fake})
		return fake
	}

	if mode == "FM_1_2" && toolName == "ship_order" && lastOK(*steps, "charge_card") == nil {
		tr.infect(checkpoint, "order shipped before the card was charged")
	}
	sent, why := tamperArgs(mode, toolName, args)
	if why != "" {
		a.log.Warnf("[agent] %s: %s", mode, why)
		tr.infect(checkpoint, why)
	}

	runs := 1
	if mode == "FM_1_3" && repeatedTools[toolName] {
		runs = 2
	}
	var result string
	for r := 0; r < runs; r++ {
		if r > 0 {
			tr.infect(checkpoint, toolName+" ran twice")
		}
		res, err := tool.Execute(ctx, sent)
		step := Step{Tool: toolName, OK: err == nil, Tampered: why != "", Args: sent, Result: res}
		if err != nil {
			a.log.Warnf("[agent] tool %s error: %v", toolName, err)
			step.Error = err.Error()
			res = jsonError(err.Error())
		} else {
			if changed, why := tamperResult(mode, toolName, res); why != "" {
				a.log.Warnf("[agent] %s: %s", mode, why)
				tr.infect(checkpoint, why)
				step.Tampered, step.Raw, step.Result, res = true, res, changed, changed
			}
			tr.checkpoint(checkpoint, "")
		}
		*steps = append(*steps, step)
		result = res
	}
	return result
}

// verify runs verifyOrder unless the fault mode skips it (FM_3_2), and
// records the ORDER_VERIFIED checkpoint.
func (a *CheckoutAgent) verify(ctx context.Context, mode string, resp *pb.PlaceOrderResponse, orderID string, steps []Step) error {
	tr := trackerFrom(ctx)
	if mode == "FM_3_2" {
		tr.infect("ORDER_VERIFIED", "final answer accepted without verification")
		return nil
	}
	if err := verifyOrder(resp, orderID, steps); err != nil {
		a.log.Errorf("[agent] order=%s rejected: %v", orderID, err)
		return err
	}
	tr.checkpoint("ORDER_VERIFIED", "")
	return nil
}

// verifyOrder checks the model's final answer against the tool calls that
// really happened, and fills in the IDs from the tools rather than the model.
// missingSteps lists the required tools that have not succeeded yet.
func missingSteps(steps []Step) []string {
	var missing []string
	for _, name := range []string{"get_cart", "charge_card", "ship_order", "empty_cart"} {
		if lastOK(steps, name) == nil {
			missing = append(missing, name)
		}
	}
	return missing
}

func verifyOrder(resp *pb.PlaceOrderResponse, orderID string, steps []Step) error {
	if missing := missingSteps(steps); len(missing) > 0 {
		return status.Errorf(codes.Internal, "checkout incomplete: %s not done", strings.Join(missing, ", "))
	}
	var shipped struct {
		TrackingID string `json:"tracking_id"`
	}
	_ = json.Unmarshal([]byte(lastOK(steps, "ship_order").Result), &shipped)
	if shipped.TrackingID == "" {
		return status.Error(codes.Internal, "ship_order returned no tracking ID")
	}
	resp.Order.OrderId = orderID
	resp.Order.ShippingTrackingId = shipped.TrackingID
	return nil
}

func lastOK(steps []Step, name string) *Step {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Tool == name && steps[i].OK {
			return &steps[i]
		}
	}
	return nil
}

func jsonError(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

// parseOrderResponse parses the model's final JSON text into a PlaceOrderResponse.
func parseOrderResponse(content string, req *pb.PlaceOrderRequest) (*pb.PlaceOrderResponse, error) {
	var result struct {
		OrderID            string `json:"order_id"`
		ShippingTrackingID string `json:"shipping_tracking_id"`
		ShippingCost       struct {
			CurrencyCode string `json:"currency_code"`
			Units        int64  `json:"units"`
			Nanos        int32  `json:"nanos"`
		} `json:"shipping_cost"`
		Items []struct {
			Item struct {
				ProductID string `json:"product_id"`
				Quantity  int32  `json:"quantity"`
			} `json:"item"`
			Cost struct {
				CurrencyCode string `json:"currency_code"`
				Units        int64  `json:"units"`
				Nanos        int32  `json:"nanos"`
			} `json:"cost"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(extractFirstJSON(content)), &result); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to parse agent final answer: %v  raw: %s", err, content)
	}

	var orderItems []*pb.OrderItem
	for _, oi := range result.Items {
		orderItems = append(orderItems, &pb.OrderItem{
			Item: &pb.CartItem{ProductId: oi.Item.ProductID, Quantity: oi.Item.Quantity},
			Cost: &pb.Money{CurrencyCode: oi.Cost.CurrencyCode, Units: oi.Cost.Units, Nanos: oi.Cost.Nanos},
		})
	}

	return &pb.PlaceOrderResponse{
		Order: &pb.OrderResult{
			OrderId:            result.OrderID,
			ShippingTrackingId: result.ShippingTrackingID,
			ShippingCost: &pb.Money{
				CurrencyCode: result.ShippingCost.CurrencyCode,
				Units:        result.ShippingCost.Units,
				Nanos:        result.ShippingCost.Nanos,
			},
			ShippingAddress: req.Address,
			Items:           orderItems,
		},
	}, nil
}

// parseArguments accepts tool arguments as either a JSON object or a JSON
// string containing an object (models emit both).
func parseArguments(args json.RawMessage) string {
	var s string
	if err := json.Unmarshal(args, &s); err == nil {
		return s
	}
	return string(args)
}

func extractFirstJSON(content string) string {
	depth := 0
	start := strings.Index(content, "{")
	if start == -1 {
		return content
	}
	inString := false
	escaped := false
	for i := start; i < len(content); i++ {
		c := content[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return strings.TrimSpace(content[start : i+1])
			}
		}
	}
	return content
}
