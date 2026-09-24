# Checkout Agent Design

## 1. Selection Rationale

The Checkout Service is selected as a candidate for agentification because its workflow involves multiple decision points and coordinates several backend services, including Cart, Product Catalog, Currency, Shipping, Payment, and Email services. This orchestration-oriented workflow provides meaningful opportunities to introduce LLM-based decision making.

## 2. Candidate Agent Tools

The current Checkout Service coordinates multiple backend microservices through gRPC. These existing backend services can potentially be exposed to the Checkout Agent as tools.

### 2.1 Cart Service — GetCart

```text
GetCartRequest
├── user_id
│
↓
CartService.GetCart()
│
↓
Cart
├── user_id
└── items
    ├── product_id
    └── quantity
```

#### References

- Request definition: [GetCartRequest](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L43-L45)
- Response definition: [Cart](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L47-L50)


### 2.2 Cart Service — EmptyCart

```text
EmptyCartRequest
└── user_id
    │
    ↓
CartService.EmptyCart()
    │
    ↓
Empty
```

#### References

- Request definition: [EmptyCartRequest](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L39-L41)
- Response definition: [Empty](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L26)


### 2.3 Product Catalog Service — GetProduct

```text
GetProductRequest
└── id
    │
    ↓
ProductCatalogService.GetProduct()
    │
    ↓
Product
├── id
├── name
├── description
├── picture
├── price_usd
└── categories
```

#### References

- Request definition: [GetProductRequest](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L93-L95)
- Response definition: [Product](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L77-L87)


### 2.4 Currency Service — Convert

```text
CurrencyConversionRequest
├── from
└── to_code
    │
    ↓
CurrencyService.Convert()
    │
    ↓
Money
├── currency_code
├── units
└── nanos
```

#### References

- Request definition: [CurrencyConversionRequest](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L168-L173)
- Response definition: [Money](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L146-L161)


### 2.5 Shipping Service — GetQuote

```text
GetQuoteRequest
├── address
└── items
    │
    ↓
ShippingService.GetQuote()
    │
    ↓
GetQuoteResponse
└── cost_usd
```

#### References

- Request definition: [GetQuoteRequest](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L112-L115)
- Response definition: [GetQuoteResponse](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L117-L119)


### 2.6 Shipping Service — ShipOrder

```text
ShipOrderRequest
├── address
└── items
    │
    ↓
ShippingService.ShipOrder()
    │
    ↓
ShipOrderResponse
└── tracking_id
```

#### References

- Request definition: [ShipOrderRequest](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L121-L124)
- Response definition: [ShipOrderResponse](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L126-L128)


### 2.7 Payment Service — Charge

```text
ChargeRequest
├── amount
└── credit_card
    ├── credit_card_number
    ├── credit_card_cvv
    ├── credit_card_expiration_year
    └── credit_card_expiration_month
    │
    ↓
PaymentService.Charge()
    │
    ↓
ChargeResponse
└── transaction_id
```

#### References

- Request definition: [ChargeRequest](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L188-L191)
- Response definition: [ChargeResponse](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L193-L195)


### 2.8 Email Service — SendOrderConfirmation

```text
SendOrderConfirmationRequest
├── email
└── order
    │
    ↓
EmailService.SendOrderConfirmation()
    │
    ↓
Empty
```

#### References

- Request definition: [SendOrderConfirmationRequest](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L216-L219)
- Response definition: [Empty](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto#L199-L201)

## 3. Current Logic

```text
Frontend / Client
      │
      │  gRPC: PlaceOrder()
      │  Request: PlaceOrderRequest
      │  ├── user_id
      │  ├── user_currency
      │  ├── address
      │  ├── email
      │  └── credit_card
      ↓
Checkout Service v1
      │
      ↓
PlaceOrder(ctx, req)
      │
      ├── Generate: Order ID
      ↓
prepareOrderItemsAndShippingQuoteFromCart(
    ctx,
    req.UserId,
    req.UserCurrency,
    req.Address
)
      │
      ├── Return: orderPrep
      ↓
Calculate Total Order Cost
      │
      ├── Result: total
      ↓
chargeCard(ctx, total, req.CreditCard)
      │
      │  Request: ChargeRequest
      │  ├── amount
      │  └── credit_card
      ↓
Call Payment Service
      │
      ├── RPC: Charge()
      └── Response: ChargeResponse
          └── transaction_id
      │
      ├── Return: Transaction ID
      ↓
shipOrder(ctx, req.Address, orderPrep.cartItems)
      │
      │  Request: ShipOrderRequest
      │  ├── address
      │  └── items
      ↓
Call Shipping Service
      │
      ├── RPC: ShipOrder()
      └── Response: ShipOrderResponse
          └── tracking_id
      │
      ├── Return: Shipping Tracking ID
      ↓
emptyUserCart(ctx, req.UserId)
      │
      │  Request: EmptyCartRequest
      │  └── user_id
      ↓
Call Cart Service
      │
      ├── RPC: EmptyCart()
      └── Response: Empty
      ↓
Create OrderResult
      │
      ├── order_id
      ├── shipping_tracking_id
      ├── shipping_cost
      ├── shipping_address
      └── items
      ↓
sendOrderConfirmation(ctx, req.Email, orderResult)
      │
      │  Request: SendOrderConfirmationRequest
      │  ├── email
      │  └── order
      ↓
Call Email Service
      │
      ├── RPC: SendOrderConfirmation()
      └── Response: Empty
      ↓
Create PlaceOrderResponse
      │
      └── order = orderResult
      ↓
Return PlaceOrderResponse
      ↓
Frontend / Client
```

### References

- Checkout Service implementation: [main.go](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/src/checkoutservice/main.go)
- gRPC request / response definitions: [demo.proto](https://github.com/GoogleCloudPlatform/microservices-demo/blob/main/protos/demo.proto)

## 4. Limitations & Agentification Opportunities

### 4.1 Limited Dynamic Decision Making

The current Checkout Service coordinates multiple backend services through a deterministic workflow.

The sequence of operations and service calls is fixed in the implementation. And it limits the system's ability to dynamically decide how to handle different checkout requests or intermediate service results.

**Agentification Opportunity:**

Introduce an AI model into the orchestration process. The model can help Agent v2 decide how to handle each checkout request and what action to take next based on the current context.

## 5. Agent v2 workflow design

```
PlaceOrderRequest
(user_id + user_currency + address + email + credit_card)
        ↓
┌──────────────────────────────────────────────────────┐
│                  Checkout Agent v2                   │
│                                                      │
│  Extract Request Context                             │
│        ↓                                             │
│  Build Agent Context                                 │
│        ↓                                             │
│  LLM / Model                                         │
│        ↓                                             │
│  Model decides how to handle                         │
│  the checkout request                                │
│        ↓                                             │
│  Execute Selected Actions                            │
│  through Backend Services                            │
│        ↓                                             │
│  Update Context with Intermediate Results            │
│        ↓                                             │
│  Continue Model-driven Decision Making               │
│  until Checkout is Completed                         │
│        ↓                                             │
│  Validate Result                                     │
│        ↓                                             │
│  Build Order Result                                  │
│                                                      │
└──────────────────────────────────────────────────────┘
        ↓
PlaceOrderResponse
(order)
```

## 6. Model / Runtime Selection

### 6.1 Model Selection

**Selected Model: Gemini**

Gemini is selected as the model for Agent v2.

### 6.2 Model Access / Runtime

The Agent will access Gemini through Google Cloud services rather than using a self-hosted model runtime.

### 6.3 Selection Rationale

Because the project already been decided to be deployed on Google Cloud Platform (GCP), using Gemini provides better integration with the existing GCP-based architecture. Additionally, it avoids introducing an additional self-hosted model runtime.