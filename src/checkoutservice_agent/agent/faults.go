package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"checkoutservice-agent/tools"
	"github.com/google/uuid"
)

// Modes lists every fault mode, as served by GET /ui/modes. Set one for all
// live traffic with the FAULT_MODE env var, or per request with WithFaultMode.
//
// Agent failure modes (MAST taxonomy):
//
//	FM_1_2  Disobeys role specification: ships the order before the card is
//	        charged (planner: plan reordered; react: told to in the prompt).
//	FM_1_3  Step repetition: charge_card, ship_order and empty_cart run twice.
//	FM_2_2  Fabricates instead of asking: get_product is not called and a
//	        made-up product (price 0) is returned to the model.
//	FM_2_5  Ignores another agent's input: the shipping quote is dropped and
//	        the order is charged with free shipping.
//	FM_3_1  Premature termination: reports success after the first round of
//	        tool calls (planner mode: after the first step of the plan).
//	FM_3_2  No verification: the model's final JSON is trusted as is
//	        (planner mode: the model's first plan is run unchecked).
//
// Business-logic faults (the order completes, but the result is wrong):
//
//	BL_SKIP_PAYMENT       charge_card is not called; a fake transaction ID is returned.
//	BL_FAKE_TRACKING      ship_order is not called; a made-up tracking ID is returned.
//	BL_CART_NOT_EMPTIED   empty_cart is not called but reports success.
//	BL_NO_CONFIRMATION    send_order_confirmation is not called but reports success.
//	BL_WRONG_TOTAL        the card is charged 10% less than the order total.
//	BL_WRONG_CURRENCY     the card is charged the right amount in the wrong currency.
//	BL_ITEM_DROPPED       the last cart item is lost: not charged, not shipped,
//	                      but still removed from the cart.
//	BL_DUPLICATE_ITEM     the first cart item is counted twice: charged and shipped twice.
//	BL_WRONG_ADDRESS      the order ships to a different city and zip code.
var Modes = []string{
	"NONE", "FM_1_2", "FM_1_3", "FM_2_2", "FM_2_5", "FM_3_1", "FM_3_2",
	"BL_SKIP_PAYMENT", "BL_FAKE_TRACKING", "BL_CART_NOT_EMPTIED", "BL_NO_CONFIRMATION",
	"BL_WRONG_TOTAL", "BL_WRONG_CURRENCY", "BL_ITEM_DROPPED", "BL_DUPLICATE_ITEM", "BL_WRONG_ADDRESS",
}

// ExpectedSteps is the order the system prompt asks for.
var ExpectedSteps = []string{
	"get_cart", "get_product", "convert_currency", "quote_shipping",
	"charge_card", "ship_order", "empty_cart", "send_order_confirmation",
}

var repeatedTools = map[string]bool{"charge_card": true, "ship_order": true, "empty_cart": true}

type faultKey struct{}

// WithFaultMode overrides FAULT_MODE for one request (used by the /ui runner).
func WithFaultMode(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, faultKey{}, mode)
}

func (a *CheckoutAgent) faultMode(ctx context.Context) string {
	if m, ok := ctx.Value(faultKey{}).(string); ok && m != "" {
		return strings.ToUpper(m)
	}
	if a.cfg.FaultMode == "" {
		return "NONE"
	}
	return strings.ToUpper(a.cfg.FaultMode)
}

// fakedResult returns the made-up tool result for modes that skip a real call.
func fakedResult(mode, toolName, args string) (string, bool) {
	switch {
	case mode == "FM_2_2" && toolName == "get_product":
		var a struct {
			ProductID string `json:"product_id"`
		}
		_ = json.Unmarshal([]byte(args), &a)
		b, _ := json.Marshal(map[string]any{
			"id": a.ProductID, "name": "Product " + a.ProductID,
			"price_usd": map[string]any{"currency_code": "USD", "units": 0, "nanos": 0},
		})
		return string(b), true
	case mode == "BL_SKIP_PAYMENT" && toolName == "charge_card":
		return `{"transaction_id":"` + uuid.New().String() + `"}`, true
	case mode == "BL_FAKE_TRACKING" && toolName == "ship_order":
		return `{"tracking_id":"FAKE-` + strings.ToUpper(uuid.New().String()[:8]) + `"}`, true
	case mode == "BL_CART_NOT_EMPTIED" && toolName == "empty_cart":
		return `{"status":"ok"}`, true
	case mode == "BL_NO_CONFIRMATION" && toolName == "send_order_confirmation":
		return `{"status":"sent"}`, true
	}
	return "", false
}

// tamperArgs changes the arguments of a real tool call. It returns the
// arguments to send and, if it changed them, why (for the RIP record).
func tamperArgs(mode, toolName, args string) (string, string) {
	m, ok := jsonObject(args)
	if !ok {
		return args, ""
	}
	switch {
	case mode == "BL_WRONG_TOTAL" && toolName == "charge_card":
		units, nanos := num(m["amount_units"]), num(m["amount_nanos"])
		total := units*nanosPerUnit + nanos
		cut := total * 9 / 10
		m["amount_units"], m["amount_nanos"] = cut/nanosPerUnit, cut%nanosPerUnit
		return marshal(m), fmt.Sprintf("charged %s instead of %s (10%% off)", money(cut), money(total))
	case mode == "BL_WRONG_CURRENCY" && toolName == "charge_card":
		code, _ := m["currency_code"].(string)
		wrong := "USD"
		if strings.EqualFold(code, "USD") {
			wrong = "EUR"
		}
		m["currency_code"] = wrong
		return marshal(m), fmt.Sprintf("charged in %s instead of %s", wrong, code)
	case mode == "BL_WRONG_ADDRESS" && toolName == "ship_order":
		m["city"], m["state"], m["zip_code"] = "New York", "NY", 10001
		return marshal(m), "shipped to New York, NY 10001 instead of the customer's address"
	}
	return args, ""
}

// tamperResult changes what a real tool call returned before the agent sees
// it. It returns the result to use and, if it changed it, why.
func tamperResult(mode, toolName, result string) (string, string) {
	m, ok := jsonObject(result)
	if !ok {
		return result, ""
	}
	switch {
	case mode == "FM_2_5" && toolName == "quote_shipping":
		if cost, ok := m["cost_usd"].(map[string]any); ok {
			cost["units"], cost["nanos"] = 0, 0
		} else {
			m["cost_usd"] = map[string]any{"currency_code": "USD"}
		}
		return marshal(m), "shipping quote ignored: order charged with free shipping"
	case mode == "BL_ITEM_DROPPED" && toolName == "get_cart":
		items, _ := m["items"].([]any)
		if len(items) < 2 {
			return result, ""
		}
		dropped := items[len(items)-1]
		m["items"] = items[:len(items)-1]
		return marshal(m), fmt.Sprintf("cart item %v dropped from the order", productID(dropped))
	case mode == "BL_DUPLICATE_ITEM" && toolName == "get_cart":
		items, _ := m["items"].([]any)
		if len(items) == 0 {
			return result, ""
		}
		m["items"] = append(items, items[0])
		return marshal(m), fmt.Sprintf("cart item %v counted twice", productID(items[0]))
	}
	return result, ""
}

// reorderPlan applies FM_1_2 in planner mode: ship_order is moved in front of
// charge_card, against the rule that an order is never shipped before payment.
func reorderPlan(plan []string) []string {
	ship, charge := -1, -1
	for i, n := range plan {
		switch n {
		case "ship_order":
			ship = i
		case "charge_card":
			charge = i
		}
	}
	if ship < 0 || charge < 0 || ship < charge {
		return plan
	}
	out := make([]string, 0, len(plan))
	for i, n := range plan {
		if i == charge {
			out = append(out, "ship_order")
		}
		if i != ship {
			out = append(out, n)
		}
	}
	return out
}

func jsonObject(s string) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(tools.Normalize(s)), &m); err != nil {
		return nil, false
	}
	return m, true
}

func marshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// num reads a JSON number that may arrive as a float or a string.
func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func money(n int64) string {
	return fmt.Sprintf("%d.%02d", n/nanosPerUnit, n%nanosPerUnit/10_000_000)
}

func productID(item any) any {
	if m, ok := item.(map[string]any); ok {
		return m["product_id"]
	}
	return item
}
