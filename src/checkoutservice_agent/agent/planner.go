package agent

// Planner mode (AGENT_MODE=planner, the default).
//
// The step-by-step ReAct loop needs about ten LLM calls per order, and a small
// model on CPU takes 10-50 s per call and often formats tool calls badly. In
// planner mode the model is asked once for the plan: which tools to call and
// in what order. The plan is checked (every required tool, dependencies
// respected), sent back once with the problems if it is wrong, and then run
// by code: arguments come from the request and earlier tool results, and the
// total is computed in code, not by the model. Fault modes still apply,
// through dispatch, and the result is verified the same way as in ReAct mode.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	pb "checkoutservice-agent/genproto"
	"checkoutservice-agent/metrics"
	"checkoutservice-agent/ollama"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const plannerPrompt = `You are the planning step of a checkout agent for an online shop.
Choose which tools to call, and in what order, to complete one checkout.

Tools:
%s
Rules:
- Use each tool once. get_product and convert_currency are repeated for every cart item automatically.
- A tool can only use results of tools listed before it.

Reply with JSON only, in this form:
{"plan": ["tool_name", "tool_name", ...]}`

// planDeps lists, for each tool, the tools whose results it needs.
var planDeps = map[string][]string{
	"get_cart":                nil,
	"get_product":             {"get_cart"},
	"convert_currency":        {"get_product"},
	"quote_shipping":          {"get_cart"},
	"charge_card":             {"convert_currency", "quote_shipping"},
	"ship_order":              {"get_cart", "charge_card"}, // never ship before payment
	"empty_cart":              {"get_cart", "charge_card"},
	"send_order_confirmation": {"ship_order", "convert_currency"},
}

// planTools are the tools a plan must contain.
var planTools = []string{"get_cart", "charge_card", "ship_order", "empty_cart"}

func (a *CheckoutAgent) toolList() string {
	var b strings.Builder
	for _, d := range a.defs {
		fmt.Fprintf(&b, "- %s: %s\n", d.Function.Name, d.Function.Description)
	}
	return b.String()
}

// askPlan gets a plan from the model, retrying once with the problems found.
// If the model cannot produce a valid plan, the standard order is used.
func (a *CheckoutAgent) askPlan(ctx context.Context, orderID, mode string) ([]string, error) {
	messages := []ollama.Message{
		{Role: "system", Content: fmt.Sprintf(plannerPrompt, a.toolList())},
		{Role: "user", Content: "Plan the checkout."},
	}
	var plan []string
	for attempt := 1; attempt <= 2; attempt++ {
		reply, err := a.llm.ChatJSON(ctx, messages)
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "LLM error: %v", err)
		}
		plan = parsePlan(reply.Content)
		a.log.Infof("[agent] order=%s plan (attempt %d): %s", orderID, attempt, strings.Join(plan, " -> "))
		if mode == "FM_3_2" {
			metrics.Plans.WithLabelValues("unchecked").Inc()
			return plan, nil // no verification: run whatever the model said
		}
		problems := checkPlan(plan)
		if len(problems) == 0 {
			outcome := "valid"
			if attempt > 1 {
				outcome = "corrected"
			}
			metrics.Plans.WithLabelValues(outcome).Inc()
			return plan, nil
		}
		a.log.Warnf("[agent] order=%s plan rejected: %s", orderID, strings.Join(problems, "; "))
		messages = append(messages, *reply, ollama.Message{Role: "user", Content: "That plan is not valid: " +
			strings.Join(problems, "; ") + ". Reply with a corrected plan, JSON only."})
	}
	a.log.Warnf("[agent] order=%s plan still invalid, using the standard order", orderID)
	metrics.Plans.WithLabelValues("fallback").Inc()
	return ExpectedSteps, nil
}

// parsePlan reads {"plan":[...]} and keeps only known tool names, once each.
func parsePlan(content string) []string {
	var out struct {
		Plan  []string `json:"plan"`
		Steps []string `json:"steps"`
	}
	_ = json.Unmarshal([]byte(extractFirstJSON(content)), &out)
	if len(out.Plan) == 0 {
		out.Plan = out.Steps
	}
	seen := map[string]bool{}
	var plan []string
	for _, name := range out.Plan {
		name = strings.TrimSpace(strings.ToLower(name))
		if _, known := planDeps[name]; known && !seen[name] {
			seen[name] = true
			plan = append(plan, name)
		}
	}
	return plan
}

// checkPlan lists what is wrong with a plan: missing required tools and tools
// placed before the tools they depend on.
func checkPlan(plan []string) []string {
	pos := map[string]int{}
	for i, name := range plan {
		pos[name] = i
	}
	var problems []string
	for _, name := range planTools {
		if _, ok := pos[name]; !ok {
			problems = append(problems, name+" is missing")
		}
	}
	for _, name := range plan {
		for _, dep := range planDeps[name] {
			if p, ok := pos[dep]; !ok || p > pos[name] {
				problems = append(problems, name+" must come after "+dep)
			}
		}
	}
	return problems
}

// checkoutState carries tool results between plan steps.
type checkoutState struct {
	cart       *pb.Cart
	products   map[string]*pb.Product
	prices     map[string]*pb.Money // converted to the user's currency
	shipping   *pb.Money            // converted to the user's currency
	trackingID string
}

func (a *CheckoutAgent) placeOrderPlanned(ctx context.Context, req *pb.PlaceOrderRequest, orderID, mode string) (*pb.PlaceOrderResponse, []Step, error) {
	plan, err := a.askPlan(ctx, orderID, mode)
	if err != nil {
		return nil, nil, err
	}
	var steps []Step
	st := &checkoutState{products: map[string]*pb.Product{}, prices: map[string]*pb.Money{}}

	for i, name := range plan {
		if err := a.runPlanStep(ctx, req, orderID, mode, name, st, &steps); err != nil {
			a.log.Errorf("[agent] order=%s step %s failed: %v", orderID, name, err)
			return nil, steps, status.Errorf(codes.Internal, "checkout step %s failed: %v", name, err)
		}
		// FM_3_1: report success after the first step of the plan.
		if mode == "FM_3_1" && i == 0 {
			a.log.Warnf("[agent] FM_3_1: terminating early for order=%s", orderID)
			return &pb.PlaceOrderResponse{Order: &pb.OrderResult{
				OrderId:         orderID,
				ShippingAddress: req.Address,
				ShippingCost:    &pb.Money{CurrencyCode: req.UserCurrency},
			}}, steps, nil
		}
	}

	resp := &pb.PlaceOrderResponse{Order: &pb.OrderResult{
		OrderId:            orderID,
		ShippingTrackingId: st.trackingID,
		ShippingCost:       st.shipping,
		ShippingAddress:    req.Address,
		Items:              st.orderItems(req.UserCurrency),
	}}
	if mode != "FM_3_2" {
		if err := verifyOrder(resp, orderID, steps); err != nil {
			a.log.Errorf("[agent] order=%s rejected: %v", orderID, err)
			return nil, steps, err
		}
	}
	a.log.Infof("[agent] order=%s completed: %d steps", orderID, len(steps))
	return resp, steps, nil
}

// runPlanStep builds the arguments for one tool from the request and earlier
// results, runs it through dispatch (so fault modes apply) and stores what
// later steps need.
func (a *CheckoutAgent) runPlanStep(ctx context.Context, req *pb.PlaceOrderRequest, orderID, mode, name string, st *checkoutState, steps *[]Step) error {
	callTool := func(tool string, args any) (string, error) {
		b, _ := json.Marshal(args)
		res := a.dispatch(ctx, mode, tool, string(b), steps)
		if s := (*steps)[len(*steps)-1]; !s.OK {
			return "", fmt.Errorf("%s", s.Error)
		}
		return res, nil
	}
	call := func(args any) (string, error) { return callTool(name, args) }
	items := st.cartItems()
	addr := map[string]any{
		"street_address": req.Address.StreetAddress, "city": req.Address.City, "state": req.Address.State,
		"country": req.Address.Country, "zip_code": req.Address.ZipCode, "items": items,
	}

	switch name {
	case "get_cart":
		res, err := call(map[string]any{"user_id": req.UserId})
		if err != nil {
			return err
		}
		st.cart = &pb.Cart{}
		if err := json.Unmarshal([]byte(res), st.cart); err != nil {
			return fmt.Errorf("bad cart: %v", err)
		}
		if len(st.cart.Items) == 0 {
			return fmt.Errorf("cart is empty")
		}
	case "get_product":
		for _, it := range st.cart.GetItems() {
			res, err := call(map[string]any{"product_id": it.ProductId})
			if err != nil {
				return err
			}
			p := &pb.Product{}
			if err := json.Unmarshal([]byte(res), p); err != nil {
				return fmt.Errorf("bad product: %v", err)
			}
			st.products[it.ProductId] = p
		}
	case "convert_currency":
		convert := func(m *pb.Money) (*pb.Money, error) {
			res, err := call(map[string]any{"units": m.GetUnits(), "nanos": m.GetNanos(),
				"from_currency": m.GetCurrencyCode(), "to_currency": req.UserCurrency})
			if err != nil {
				return nil, err
			}
			out := &pb.Money{}
			if err := json.Unmarshal([]byte(res), out); err != nil {
				return nil, fmt.Errorf("bad money: %v", err)
			}
			return out, nil
		}
		for id, p := range st.products {
			m, err := convert(p.GetPriceUsd())
			if err != nil {
				return err
			}
			st.prices[id] = m
		}
		if st.shipping != nil && st.shipping.CurrencyCode != req.UserCurrency {
			m, err := convert(st.shipping)
			if err != nil {
				return err
			}
			st.shipping = m
		}
	case "quote_shipping":
		res, err := call(addr)
		if err != nil {
			return err
		}
		q := &pb.GetQuoteResponse{}
		if err := json.Unmarshal([]byte(res), q); err != nil {
			return fmt.Errorf("bad quote: %v", err)
		}
		st.shipping = q.CostUsd
		if st.shipping != nil && st.shipping.CurrencyCode == "" {
			st.shipping.CurrencyCode = "USD"
		}
		// Quote taken after the prices were converted: convert it on its own.
		if len(st.prices) > 0 && st.shipping.GetCurrencyCode() != req.UserCurrency {
			res, err := callTool("convert_currency", map[string]any{"units": st.shipping.GetUnits(), "nanos": st.shipping.GetNanos(),
				"from_currency": st.shipping.GetCurrencyCode(), "to_currency": req.UserCurrency})
			if err != nil {
				return err
			}
			m := &pb.Money{}
			if err := json.Unmarshal([]byte(res), m); err != nil {
				return fmt.Errorf("bad money: %v", err)
			}
			st.shipping = m
		}
	case "charge_card":
		total := st.total(req.UserCurrency)
		_, err := call(map[string]any{"amount_units": total.Units, "amount_nanos": total.Nanos,
			"currency_code": total.CurrencyCode})
		return err
	case "ship_order":
		res, err := call(addr)
		if err != nil {
			return err
		}
		var out struct {
			TrackingID string `json:"tracking_id"`
		}
		_ = json.Unmarshal([]byte(res), &out)
		st.trackingID = out.TrackingID
	case "empty_cart":
		_, err := call(map[string]any{"user_id": req.UserId})
		return err
	case "send_order_confirmation":
		var its []map[string]any
		for _, oi := range st.orderItems(req.UserCurrency) {
			its = append(its, map[string]any{
				"item": map[string]any{"product_id": oi.Item.ProductId, "quantity": oi.Item.Quantity},
				"cost": map[string]any{"units": oi.Cost.Units, "nanos": oi.Cost.Nanos, "currency_code": oi.Cost.CurrencyCode},
			})
		}
		ship := st.shipping
		if ship == nil {
			ship = &pb.Money{CurrencyCode: req.UserCurrency}
		}
		_, err := call(map[string]any{"email": req.Email, "order_id": orderID, "shipping_tracking_id": st.trackingID,
			"shipping_cost_units": ship.Units, "shipping_cost_nanos": ship.Nanos, "shipping_currency": ship.CurrencyCode,
			"items": its})
		return err
	default:
		return fmt.Errorf("unknown tool %s", name)
	}
	return nil
}

func (st *checkoutState) cartItems() []map[string]any {
	var items []map[string]any
	for _, it := range st.cart.GetItems() {
		items = append(items, map[string]any{"product_id": it.ProductId, "quantity": it.Quantity})
	}
	return items
}

func (st *checkoutState) orderItems(currency string) []*pb.OrderItem {
	var out []*pb.OrderItem
	for _, it := range st.cart.GetItems() {
		cost := st.prices[it.ProductId]
		if cost == nil {
			cost = &pb.Money{CurrencyCode: currency}
		}
		out = append(out, &pb.OrderItem{Item: it, Cost: cost})
	}
	return out
}

// total is the sum of price x quantity plus shipping, in the user's currency.
func (st *checkoutState) total(currency string) *pb.Money {
	total := &pb.Money{CurrencyCode: currency}
	for _, it := range st.cart.GetItems() {
		if p := st.prices[it.ProductId]; p != nil {
			total = moneyAdd(total, moneyMul(p, int64(it.Quantity)))
		}
	}
	if st.shipping != nil {
		total = moneyAdd(total, st.shipping)
	}
	return total
}

const nanosPerUnit = 1_000_000_000

func moneyAdd(a, b *pb.Money) *pb.Money {
	n := (a.Units*nanosPerUnit + int64(a.Nanos)) + (b.Units*nanosPerUnit + int64(b.Nanos))
	return &pb.Money{CurrencyCode: a.CurrencyCode, Units: n / nanosPerUnit, Nanos: int32(n % nanosPerUnit)}
}

func moneyMul(m *pb.Money, q int64) *pb.Money {
	n := (m.Units*nanosPerUnit + int64(m.Nanos)) * q
	return &pb.Money{CurrencyCode: m.CurrencyCode, Units: n / nanosPerUnit, Nanos: int32(n % nanosPerUnit)}
}
