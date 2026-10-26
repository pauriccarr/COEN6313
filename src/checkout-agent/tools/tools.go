// Package tools wraps every downstream gRPC call as an LLM-callable tool.
// Each Tool has:
//   - a Definition() for the Ollama tool schema
//   - an Execute(ctx, argsJSON) that performs the real gRPC call and returns
//     a JSON-serialisable result.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	pb "checkoutservice-agent/genproto"
	"checkoutservice-agent/ollama"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Tool is the interface every checkout tool must satisfy.
type Tool interface {
	Name() string
	Definition() ollama.ToolDefinition
	Execute(ctx context.Context, argsJSON string) (string, error)
}

// ---- GetCart ----------------------------------------------------------------

type GetCartTool struct {
	conn pb.CartServiceClient
}

func NewGetCartTool(addr string) (*GetCartTool, error) {
	c, err := dial(addr)
	if err != nil {
		return nil, err
	}
	return &GetCartTool{conn: pb.NewCartServiceClient(c)}, nil
}

func (t *GetCartTool) Name() string { return "get_cart" }

func (t *GetCartTool) Definition() ollama.ToolDefinition {
	return ollama.ToolDefinition{
		Type: "function",
		Function: ollama.FunctionMeta{
			Name:        t.Name(),
			Description: "Retrieves the shopping cart items for a given user ID.",
			Parameters: jsonSchema(map[string]any{
				"user_id": param("string", "The user's unique identifier"),
			}, []string{"user_id"}),
		},
	}
}

func (t *GetCartTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	resp, err := t.conn.GetCart(ctx, &pb.GetCartRequest{UserId: args.UserID})
	if err != nil {
		return "", fmt.Errorf("GetCart: %w", err)
	}
	return marshalJSON(resp)
}

// ---- EmptyCart --------------------------------------------------------------

type EmptyCartTool struct {
	conn pb.CartServiceClient
}

func NewEmptyCartTool(addr string) (*EmptyCartTool, error) {
	c, err := dial(addr)
	if err != nil {
		return nil, err
	}
	return &EmptyCartTool{conn: pb.NewCartServiceClient(c)}, nil
}

func (t *EmptyCartTool) Name() string { return "empty_cart" }

func (t *EmptyCartTool) Definition() ollama.ToolDefinition {
	return ollama.ToolDefinition{
		Type: "function",
		Function: ollama.FunctionMeta{
			Name:        t.Name(),
			Description: "Empties the shopping cart for a user after a successful order.",
			Parameters: jsonSchema(map[string]any{
				"user_id": param("string", "The user's unique identifier"),
			}, []string{"user_id"}),
		},
	}
}

func (t *EmptyCartTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	_, err := t.conn.EmptyCart(ctx, &pb.EmptyCartRequest{UserId: args.UserID})
	if err != nil {
		return "", fmt.Errorf("EmptyCart: %w", err)
	}
	return `{"status":"ok"}`, nil
}

// ---- GetProduct -------------------------------------------------------------

type GetProductTool struct {
	conn pb.ProductCatalogServiceClient
}

func NewGetProductTool(addr string) (*GetProductTool, error) {
	c, err := dial(addr)
	if err != nil {
		return nil, err
	}
	return &GetProductTool{conn: pb.NewProductCatalogServiceClient(c)}, nil
}

func (t *GetProductTool) Name() string { return "get_product" }

func (t *GetProductTool) Definition() ollama.ToolDefinition {
	return ollama.ToolDefinition{
		Type: "function",
		Function: ollama.FunctionMeta{
			Name:        t.Name(),
			Description: "Fetches product details (name, price, etc.) by product ID.",
			Parameters: jsonSchema(map[string]any{
				"product_id": param("string", "The product's unique identifier"),
			}, []string{"product_id"}),
		},
	}
}

func (t *GetProductTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		ProductID string `json:"product_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	resp, err := t.conn.GetProduct(ctx, &pb.GetProductRequest{Id: args.ProductID})
	if err != nil {
		return "", fmt.Errorf("GetProduct: %w", err)
	}
	return marshalJSON(resp)
}

// ---- ConvertCurrency --------------------------------------------------------

type ConvertCurrencyTool struct {
	conn pb.CurrencyServiceClient
}

func NewConvertCurrencyTool(addr string) (*ConvertCurrencyTool, error) {
	c, err := dial(addr)
	if err != nil {
		return nil, err
	}
	return &ConvertCurrencyTool{conn: pb.NewCurrencyServiceClient(c)}, nil
}

func (t *ConvertCurrencyTool) Name() string { return "convert_currency" }

func (t *ConvertCurrencyTool) Definition() ollama.ToolDefinition {
	return ollama.ToolDefinition{
		Type: "function",
		Function: ollama.FunctionMeta{
			Name:        t.Name(),
			Description: "Converts a monetary amount from one currency to another.",
			Parameters: jsonSchema(map[string]any{
				"units":         param("integer", "Whole units of the source amount"),
				"nanos":         param("integer", "Fractional nanos of the source amount"),
				"from_currency": param("string", "ISO 4217 source currency code"),
				"to_currency":   param("string", "ISO 4217 target currency code"),
			}, []string{"units", "nanos", "from_currency", "to_currency"}),
		},
	}
}

func (t *ConvertCurrencyTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Units        int64  `json:"units"`
		Nanos        int32  `json:"nanos"`
		FromCurrency string `json:"from_currency"`
		ToCurrency   string `json:"to_currency"`
	}

	fixedJSON, err := normalizeJSON(argsJSON)
	if err != nil {
		return "", err
	}

	if err := json.Unmarshal([]byte(fixedJSON), &args); err != nil {
		return "", err
	}


	resp, err := t.conn.Convert(ctx, &pb.CurrencyConversionRequest{
		From: &pb.Money{
			CurrencyCode: args.FromCurrency,
			Units:        args.Units,
			Nanos:        args.Nanos,
		},
		ToCode: args.ToCurrency,
	})
	if err != nil {
		return "", fmt.Errorf("ConvertCurrency: %w", err)
	}
	return marshalJSON(resp)
}

// ---- QuoteShipping ----------------------------------------------------------

type QuoteShippingTool struct {
	conn pb.ShippingServiceClient
}

func NewQuoteShippingTool(addr string) (*QuoteShippingTool, error) {
	c, err := dial(addr)
	if err != nil {
		return nil, err
	}
	return &QuoteShippingTool{conn: pb.NewShippingServiceClient(c)}, nil
}

func (t *QuoteShippingTool) Name() string { return "quote_shipping" }

func (t *QuoteShippingTool) Definition() ollama.ToolDefinition {
	return ollama.ToolDefinition{
		Type: "function",
		Function: ollama.FunctionMeta{
			Name:        t.Name(),
			Description: "Gets a shipping cost quote for a set of cart items to a delivery address.",
			Parameters: jsonSchema(map[string]any{
				"street_address": param("string", "Street address line 1"),
				"city":           param("string", "City name"),
				"state":          param("string", "State or province"),
				"country":        param("string", "Country code"),
				"zip_code":       param("integer", "Postal/zip code as integer"),
				"items":          param("array", "Array of {product_id, quantity} objects to ship"),
			}, []string{"street_address", "city", "state", "country", "zip_code", "items"}),
		},
	}
}

func (t *QuoteShippingTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		StreetAddress string `json:"street_address"`
		City          string `json:"city"`
		State         string `json:"state"`
		Country       string `json:"country"`
		ZipCode       int32  `json:"zip_code"`
		Items         []struct {
			ProductID string `json:"product_id"`
			Quantity  int32  `json:"quantity"`
		} `json:"items"`
	}

        fixedJSON, err := normalizeJSON(argsJSON)
	if err != nil {
		return "", err
	}

	if err := json.Unmarshal([]byte(fixedJSON), &args); err != nil {
		return "", err
	}


	var cartItems []*pb.CartItem
	for _, it := range args.Items {
		cartItems = append(cartItems, &pb.CartItem{
			ProductId: it.ProductID,
			Quantity:  it.Quantity,
		})
	}

	resp, err := t.conn.GetQuote(ctx, &pb.GetQuoteRequest{
		Address: &pb.Address{
			StreetAddress: args.StreetAddress,
			City:          args.City,
			State:         args.State,
			Country:       args.Country,
			ZipCode:       args.ZipCode,
		},
		Items: cartItems,
	})
	if err != nil {
		return "", fmt.Errorf("QuoteShipping: %w", err)
	}
	return marshalJSON(resp)
}

// ---- ShipOrder --------------------------------------------------------------

type ShipOrderTool struct {
	conn pb.ShippingServiceClient
}

func NewShipOrderTool(addr string) (*ShipOrderTool, error) {
	c, err := dial(addr)
	if err != nil {
		return nil, err
	}
	return &ShipOrderTool{conn: pb.NewShippingServiceClient(c)}, nil
}

func (t *ShipOrderTool) Name() string { return "ship_order" }

func (t *ShipOrderTool) Definition() ollama.ToolDefinition {
	return ollama.ToolDefinition{
		Type: "function",
		Function: ollama.FunctionMeta{
			Name:        t.Name(),
			Description: "Ships the order to the specified address and returns a tracking ID.",
			Parameters: jsonSchema(map[string]any{
				"street_address": param("string", "Street address"),
				"city":           param("string", "City"),
				"state":          param("string", "State"),
				"country":        param("string", "Country"),
				"zip_code":       param("integer", "Zip code as integer"),
				"items":          param("array", "Array of {product_id, quantity} to ship"),
			}, []string{"street_address", "city", "state", "country", "zip_code", "items"}),
		},
	}
}

func (t *ShipOrderTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		StreetAddress string `json:"street_address"`
		City          string `json:"city"`
		State         string `json:"state"`
		Country       string `json:"country"`
		ZipCode       int32  `json:"zip_code"`
		Items         []struct {
			ProductID string `json:"product_id"`
			Quantity  int32  `json:"quantity"`
		} `json:"items"`
	}

	fixedJSON, err := normalizeJSON(argsJSON)
	if err != nil {
    		return "", err
	}

	if err := json.Unmarshal([]byte(fixedJSON), &args); err != nil {
    		return "", err
	}	


	var cartItems []*pb.CartItem
	for _, it := range args.Items {
		cartItems = append(cartItems, &pb.CartItem{
			ProductId: it.ProductID,
			Quantity:  it.Quantity,
		})
	}

	resp, err := t.conn.ShipOrder(ctx, &pb.ShipOrderRequest{
		Address: &pb.Address{
			StreetAddress: args.StreetAddress,
			City:          args.City,
			State:         args.State,
			Country:       args.Country,
			ZipCode:       args.ZipCode,
		},
		Items: cartItems,
	})
	if err != nil {
		return "", fmt.Errorf("ShipOrder: %w", err)
	}
	return marshalJSON(resp)
}

// ---- ChargeCard -------------------------------------------------------------

type ChargeCardTool struct {
	conn pb.PaymentServiceClient
}

func NewChargeCardTool(addr string) (*ChargeCardTool, error) {
	c, err := dial(addr)
	if err != nil {
		return nil, err
	}
	return &ChargeCardTool{conn: pb.NewPaymentServiceClient(c)}, nil
}

func (t *ChargeCardTool) Name() string { return "charge_card" }

func (t *ChargeCardTool) Definition() ollama.ToolDefinition {
	return ollama.ToolDefinition{
		Type: "function",
		Function: ollama.FunctionMeta{
			Name:        t.Name(),
			Description: "Charges the customer's credit card for the given amount. Returns a transaction ID.",
			Parameters: jsonSchema(map[string]any{
				"amount_units":    param("integer", "Whole currency units to charge"),
				"amount_nanos":    param("integer", "Fractional nanos"),
				"currency_code":   param("string", "ISO 4217 currency code"),
				"card_number":     param("string", "Credit card number"),
				"card_expiry_year":  param("integer", "Card expiry year (YYYY)"),
				"card_expiry_month": param("integer", "Card expiry month (1-12)"),
				"card_cvv":        param("integer", "CVV code as integer"),
			}, []string{"amount_units", "amount_nanos", "currency_code",
				"card_number", "card_expiry_year", "card_expiry_month", "card_cvv"}),
		},
	}
}

func (t *ChargeCardTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		AmountUnits      int64  `json:"amount_units"`
		AmountNanos      int32  `json:"amount_nanos"`
		CurrencyCode     string `json:"currency_code"`
		CardNumber       string `json:"card_number"`
		CardExpiryYear   int32  `json:"card_expiry_year"`
		CardExpiryMonth  int32  `json:"card_expiry_month"`
		CardCVV          int32  `json:"card_cvv"`
	}

	fixedJSON, err := normalizeJSON(argsJSON)
	if err != nil {
		return "", err
	}

	if err := json.Unmarshal([]byte(fixedJSON), &args); err != nil {
		return "", err
	}
	
	resp, err := t.conn.Charge(ctx, &pb.ChargeRequest{
		Amount: &pb.Money{
			CurrencyCode: args.CurrencyCode,
			Units:        args.AmountUnits,
			Nanos:        args.AmountNanos,
		},
		CreditCard: &pb.CreditCardInfo{
			CreditCardNumber:          args.CardNumber,
			CreditCardExpirationYear:  args.CardExpiryYear,
			CreditCardExpirationMonth: args.CardExpiryMonth,
			CreditCardCvv:             args.CardCVV,
		},
	})
	if err != nil {
		return "", fmt.Errorf("ChargeCard: %w", err)
	}
	return marshalJSON(resp)
}

// ---- SendOrderConfirmation --------------------------------------------------

type SendConfirmationTool struct {
	conn pb.EmailServiceClient
}

func NewSendConfirmationTool(addr string) (*SendConfirmationTool, error) {
	c, err := dial(addr)
	if err != nil {
		return nil, err
	}
	return &SendConfirmationTool{conn: pb.NewEmailServiceClient(c)}, nil
}

func (t *SendConfirmationTool) Name() string { return "send_order_confirmation" }

func (t *SendConfirmationTool) Definition() ollama.ToolDefinition {
	return ollama.ToolDefinition{
		Type: "function",
		Function: ollama.FunctionMeta{
			Name:        t.Name(),
			Description: "Sends an order confirmation email to the customer.",
			Parameters: jsonSchema(map[string]any{
				"email":        param("string", "Customer email address"),
				"order_id":     param("string", "The order's unique identifier"),
				"shipping_tracking_id": param("string", "Shipping tracking ID"),
				"shipping_cost_units": param("integer", "Shipping cost whole units"),
				"shipping_cost_nanos": param("integer", "Shipping cost nanos"),
				"shipping_currency":   param("string", "Shipping cost currency"),
				"items":        param("array", "Array of order item objects"),
			}, []string{"email", "order_id", "shipping_tracking_id",
				"shipping_cost_units", "shipping_cost_nanos", "shipping_currency", "items"}),
		},
	}
}

func (t *SendConfirmationTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Email               string `json:"email"`
		OrderID             string `json:"order_id"`
		ShippingTrackingID  string `json:"shipping_tracking_id"`
		ShippingCostUnits   int64  `json:"shipping_cost_units"`
		ShippingCostNanos   int32  `json:"shipping_cost_nanos"`
		ShippingCurrency    string `json:"shipping_currency"`
		Items []struct {
			Item struct {
				ProductID string `json:"product_id"`
				Quantity  int32  `json:"quantity"`
			} `json:"item"`
			Cost struct {
				Units        int64  `json:"units"`
				Nanos        int32  `json:"nanos"`
				CurrencyCode string `json:"currency_code"`
			} `json:"cost"`
		} `json:"items"`
	}
	fixedJSON, err := normalizeJSON(argsJSON)
	if err != nil {
		return "", err
	}

	if err := json.Unmarshal([]byte(fixedJSON), &args); err != nil {
		return "", err
	}


	var orderItems []*pb.OrderItem
	for _, oi := range args.Items {
		orderItems = append(orderItems, &pb.OrderItem{
			Item: &pb.CartItem{ProductId: oi.Item.ProductID, Quantity: oi.Item.Quantity},
			Cost: &pb.Money{CurrencyCode: oi.Cost.CurrencyCode, Units: oi.Cost.Units, Nanos: oi.Cost.Nanos},
		})
	}

	_, err = t.conn.SendOrderConfirmation(ctx, &pb.SendOrderConfirmationRequest{
		Email: args.Email,
		Order: &pb.OrderResult{
			OrderId:            args.OrderID,
			ShippingTrackingId: args.ShippingTrackingID,
			ShippingCost: &pb.Money{
				CurrencyCode: args.ShippingCurrency,
				Units:        args.ShippingCostUnits,
				Nanos:        args.ShippingCostNanos,
			},
			Items: orderItems,
		},
	})
	if err != nil {
		return "", fmt.Errorf("SendOrderConfirmation: %w", err)
	}
	return `{"status":"sent"}`, nil
}

// ---- helpers ----------------------------------------------------------------

func dial(addr string) (*grpc.ClientConn, error) {
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return conn, nil
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func param(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func jsonSchema(props map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}
// normalizeJSON attempts to fix common LLM JSON mistakes:
// - numbers sent as strings ? convert to numbers
// - arrays sent as stringified JSON ? decode them
func normalizeValue(v any) any {
	switch val := v.(type) {

	case map[string]any:
		for k, v2 := range val {
			val[k] = normalizeValue(v2)
		}
		return val

	case []any:
		for i, v2 := range val {
			val[i] = normalizeValue(v2)
		}
		return val

	case string:
		// Try int
		var i int64
		if _, err := fmt.Sscanf(val, "%d", &i); err == nil {
			return i
		}

		// Try float
		var f float64
		if _, err := fmt.Sscanf(val, "%f", &f); err == nil {
			return f
		}

		// Try JSON decode (array/object inside string)
		var parsed any
		if err := json.Unmarshal([]byte(val), &parsed); err == nil {
			return normalizeValue(parsed)
		}

		return val

	default:
		return val
	}
}

func normalizeJSON(input string) (string, error) {
	var raw any
	if err := json.Unmarshal([]byte(input), &raw); err != nil {
		return "", err
	}

	raw = normalizeValue(raw)

	out, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
