package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// Modes lists every fault mode, as served by GET /ui/modes.
//
//	FM_1_3  Step repetition: charge_card, ship_order and empty_cart run twice.
//	FM_2_2  Fabricates instead of asking: get_product is not called and a
//	        made-up product (price 0) is returned to the model.
//	FM_3_1  Premature termination: reports success after the first round of
//	        tool calls.
//	FM_3_2  No verification: the model's final JSON is trusted as is.
//	BL_SKIP_PAYMENT       charge_card is not called; a fake transaction ID is returned.
//	BL_FAKE_TRACKING      ship_order is not called; a made-up tracking ID is returned.
//	BL_CART_NOT_EMPTIED   empty_cart is not called but reports success.
//	BL_NO_CONFIRMATION    send_order_confirmation is not called but reports success.
var Modes = []string{
	"NONE", "FM_1_3", "FM_2_2", "FM_3_1", "FM_3_2",
	"BL_SKIP_PAYMENT", "BL_FAKE_TRACKING", "BL_CART_NOT_EMPTIED", "BL_NO_CONFIRMATION",
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
		return m
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
