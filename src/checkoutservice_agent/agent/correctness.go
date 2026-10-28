package agent

// Correctness checks for the canary. An agent can return a successful
// response that is wrong, so every order the agent accepts is checked against
// rules the response and the stored data must satisfy. The checks call the
// services directly (not through dispatch), so fault modes cannot hide from them.
//
//	charge_total   the amount charged equals catalog price x quantity plus the
//	               shipping quote, converted to the user's currency (recomputed
//	               here from the services, not taken from the agent)
//	valid_products every product in the response exists in the catalog
//	paid_once      exactly one real charge_card call succeeded
//	shipped_once   exactly one real ship_order call succeeded, and the tracking
//	               ID in the response is the one it returned
//	cart_emptied   the user's cart is empty after the order
//	confirmation   the confirmation email was really sent
//	items_match_cart  the order has exactly the items and quantities the cart
//	               service returned (its real answer, even if a fault changed
//	               what the agent saw)
//	ship_address   the order was shipped to the address in the request
//	paid_before_ship  the card was charged before the order was shipped

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	pb "checkoutservice-agent/genproto"
	"checkoutservice-agent/metrics"
	"checkoutservice-agent/tools"
)

// CheckResult is the outcome of one rule.
type CheckResult struct {
	Rule   string `json:"rule"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// CheckOrder runs every rule on an accepted order, records the results in the
// metrics and returns them.
func (a *CheckoutAgent) CheckOrder(ctx context.Context, req *pb.PlaceOrderRequest, resp *pb.PlaceOrderResponse, steps []Step) []CheckResult {
	results := a.Check(ctx, req, resp, steps)
	correct := "correct"
	for _, r := range results {
		result := "pass"
		if !r.Pass {
			result, correct = "fail", "incorrect"
		}
		metrics.CorrectnessChecks.WithLabelValues(r.Rule, result).Inc()
	}
	metrics.OrdersCorrect.WithLabelValues(correct).Inc()
	return results
}

// Check runs every rule on an accepted order without touching the metrics
// (the fault runner uses it, so test orders do not count as live traffic).
func (a *CheckoutAgent) Check(ctx context.Context, req *pb.PlaceOrderRequest, resp *pb.PlaceOrderResponse, steps []Step) []CheckResult {
	var results []CheckResult
	add := func(rule string, pass bool, detail string, args ...any) {
		r := CheckResult{Rule: rule, Pass: pass}
		if !pass {
			r.Detail = fmt.Sprintf(detail, args...)
		}
		results = append(results, r)
	}
	order := resp.GetOrder()

	// Real (not faked) successful calls of each tool.
	real := map[string][]Step{}
	for _, s := range steps {
		if s.OK && !s.Faked {
			real[s.Tool] = append(real[s.Tool], s)
		}
	}

	expected, err := a.expectedTotal(ctx, req, order)
	add("valid_products", err == nil, "%v", err)
	if charges := real["charge_card"]; err == nil && len(charges) > 0 {
		var charged struct {
			Units int64  `json:"amount_units"`
			Nanos int32  `json:"amount_nanos"`
			Code  string `json:"currency_code"`
		}
		_ = json.Unmarshal([]byte(charges[0].Args), &charged)
		got := float64(charged.Units) + float64(charged.Nanos)/nanosPerUnit
		want := float64(expected.Units) + float64(expected.Nanos)/nanosPerUnit
		add("charge_total", charged.Code == expected.CurrencyCode && math.Abs(got-want) < 0.011,
			"charged %.2f %s, expected %.2f %s", got, charged.Code, want, expected.CurrencyCode)
	} else {
		add("charge_total", false, "no real charge to compare (%v)", err)
	}

	add("paid_once", len(real["charge_card"]) == 1, "%d real charges", len(real["charge_card"]))

	ships := real["ship_order"]
	tracking := ""
	if len(ships) > 0 {
		var out struct {
			TrackingID string `json:"tracking_id"`
		}
		_ = json.Unmarshal([]byte(ships[0].Result), &out)
		tracking = out.TrackingID
	}
	add("shipped_once", len(ships) == 1 && tracking != "" && order.GetShippingTrackingId() == tracking,
		"%d real shipments, response tracking %q, ship_order returned %q", len(ships), order.GetShippingTrackingId(), tracking)

	if res, err := a.tools["get_cart"].Execute(ctx, fmt.Sprintf(`{"user_id":%q}`, req.UserId)); err != nil {
		add("cart_emptied", false, "get_cart: %v", err)
	} else {
		var cart pb.Cart
		_ = json.Unmarshal([]byte(res), &cart)
		add("cart_emptied", len(cart.Items) == 0, "%d items still in the cart", len(cart.Items))
	}

	add("confirmation", len(real["send_order_confirmation"]) > 0, "no confirmation email sent")

	if carts := real["get_cart"]; len(carts) > 0 {
		raw := carts[0].Result
		if carts[0].Raw != "" {
			raw = carts[0].Raw
		}
		var cart pb.Cart
		_ = json.Unmarshal([]byte(raw), &cart)
		want, got := quantities(cart.Items), map[string]int32{}
		for _, oi := range order.GetItems() {
			got[oi.GetItem().GetProductId()] += oi.GetItem().GetQuantity()
		}
		add("items_match_cart", fmt.Sprint(want) == fmt.Sprint(got), "cart had %v, order has %v", want, got)
	} else {
		add("items_match_cart", false, "the cart was never read")
	}

	if ships := real["ship_order"]; len(ships) > 0 {
		var to pb.Address
		_ = json.Unmarshal([]byte(tools.Normalize(ships[0].Args)), &to)
		want := req.GetAddress()
		add("ship_address", to.City == want.City && to.ZipCode == want.ZipCode && to.StreetAddress == want.StreetAddress,
			"shipped to %s, %s %d; customer is at %s, %s %d", to.StreetAddress, to.City, to.ZipCode,
			want.StreetAddress, want.City, want.ZipCode)
	}

	charged, shipped := -1, -1
	for i, s := range steps {
		if s.OK && !s.Faked && s.Tool == "charge_card" && charged < 0 {
			charged = i
		}
		if s.OK && !s.Faked && s.Tool == "ship_order" && shipped < 0 {
			shipped = i
		}
	}
	if charged >= 0 && shipped >= 0 {
		add("paid_before_ship", charged < shipped, "order shipped before the card was charged")
	}
	return results
}

func quantities(items []*pb.CartItem) map[string]int32 {
	out := map[string]int32{}
	for _, it := range items {
		out[it.GetProductId()] += it.GetQuantity()
	}
	return out
}

// expectedTotal recomputes what the order should cost from the catalog, the
// shipping quote and the currency service.
func (a *CheckoutAgent) expectedTotal(ctx context.Context, req *pb.PlaceOrderRequest, order *pb.OrderResult) (*pb.Money, error) {
	if len(order.GetItems()) == 0 {
		return nil, fmt.Errorf("order has no items")
	}
	convert := func(m *pb.Money) (*pb.Money, error) {
		if m.GetCurrencyCode() == req.UserCurrency {
			return m, nil
		}
		res, err := a.tools["convert_currency"].Execute(ctx, fmt.Sprintf(
			`{"units":%d,"nanos":%d,"from_currency":%q,"to_currency":%q}`,
			m.GetUnits(), m.GetNanos(), m.GetCurrencyCode(), req.UserCurrency))
		if err != nil {
			return nil, err
		}
		out := &pb.Money{}
		return out, json.Unmarshal([]byte(res), out)
	}

	total := &pb.Money{CurrencyCode: req.UserCurrency}
	var items []map[string]any
	for _, oi := range order.Items {
		id := oi.GetItem().GetProductId()
		res, err := a.tools["get_product"].Execute(ctx, fmt.Sprintf(`{"product_id":%q}`, id))
		if err != nil {
			return nil, fmt.Errorf("product %q not in the catalog: %v", id, err)
		}
		var p pb.Product
		if err := json.Unmarshal([]byte(res), &p); err != nil {
			return nil, err
		}
		price, err := convert(p.GetPriceUsd())
		if err != nil {
			return nil, err
		}
		total = moneyAdd(total, moneyMul(price, int64(oi.GetItem().GetQuantity())))
		items = append(items, map[string]any{"product_id": id, "quantity": oi.GetItem().GetQuantity()})
	}

	args, _ := json.Marshal(map[string]any{
		"street_address": req.Address.StreetAddress, "city": req.Address.City, "state": req.Address.State,
		"country": req.Address.Country, "zip_code": req.Address.ZipCode, "items": items,
	})
	res, err := a.tools["quote_shipping"].Execute(ctx, string(args))
	if err != nil {
		return nil, err
	}
	var q pb.GetQuoteResponse
	if err := json.Unmarshal([]byte(res), &q); err != nil {
		return nil, err
	}
	if q.CostUsd != nil && q.CostUsd.CurrencyCode == "" {
		q.CostUsd.CurrencyCode = "USD"
	}
	ship, err := convert(q.CostUsd)
	if err != nil {
		return nil, err
	}
	return moneyAdd(total, ship), nil
}
