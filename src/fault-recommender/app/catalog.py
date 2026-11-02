"""
Static knowledge the recommender reasons over.

  BOUTIQUE_RPCS   gRPC surface of each Online Boutique service (from demo.proto)
  MAST_MODES      MAST failure taxonomy for LLM agents (Cemri et al., 2025)
  INFRA_FAULTS    Classic fault-injection patterns for any networked service
"""

# service base name -> (proto service, port, RPC methods)
BOUTIQUE_RPCS = {
    "cart":           ("hipstershop.CartService", 7070, ["AddItem", "GetCart", "EmptyCart"]),
    "recommendation": ("hipstershop.RecommendationService", 8080, ["ListRecommendations"]),
    "productcatalog": ("hipstershop.ProductCatalogService", 3550, ["ListProducts", "GetProduct", "SearchProducts"]),
    "shipping":       ("hipstershop.ShippingService", 50051, ["GetQuote", "ShipOrder"]),
    "currency":       ("hipstershop.CurrencyService", 7000, ["GetSupportedCurrencies", "Convert"]),
    "payment":        ("hipstershop.PaymentService", 50051, ["Charge"]),
    "email":          ("hipstershop.EmailService", 5000, ["SendOrderConfirmation"]),
    "checkout":       ("hipstershop.CheckoutService", 5050, ["PlaceOrder"]),
    "ad":             ("hipstershop.AdService", 9555, ["GetAds"]),
    "frontend":       ("HTTP", 8080, ["GET /", "GET /product/{id}", "POST /cart", "POST /cart/checkout",
                                      "POST /setCurrency"]),
}

# What each RPC returns, so the LLM can invent realistic business-logic (BL_*) faults
RPC_PAYLOADS = {
    "AddItem": "user_id, item{product_id, quantity}",
    "GetCart": "Cart{user_id, items[{product_id, quantity}]}",
    "EmptyCart": "Empty",
    "ListRecommendations": "product_ids[]",
    "ListProducts": "products[{id, name, description, picture, price_usd{currency_code, units, nanos}, categories[]}]",
    "GetProduct": "Product{id, name, price_usd, categories}",
    "SearchProducts": "results[Product]",
    "GetQuote": "cost_usd{currency_code, units, nanos}",
    "ShipOrder": "tracking_id",
    "GetSupportedCurrencies": "currency_codes[]",
    "Convert": "Money{currency_code, units, nanos}",
    "Charge": "transaction_id (input: amount, credit_card{number, cvv, expiration})",
    "SendOrderConfirmation": "Empty (input: email, OrderResult{order_id, shipping_tracking_id, items, total})",
    "PlaceOrder": "OrderResult{order_id, shipping_tracking_id, shipping_cost, shipping_address, items}",
    "GetAds": "ads[{redirect_url, text}] (input: context_keys[])",
}

MAST_MODES = {
    # 1. Specification and system design
    "FM_1_1": "Disobey task specification",
    "FM_1_2": "Disobey role specification (wrong action / routing)",
    "FM_1_3": "Step repetition",
    "FM_1_4": "Loss of conversation history / context",
    "FM_1_5": "Unaware of termination conditions",
    # 2. Inter-agent misalignment
    "FM_2_1": "Conversation reset",
    "FM_2_2": "Fail to ask for clarification (fabricates instead)",
    "FM_2_3": "Task derailment",
    "FM_2_4": "Information withholding",
    "FM_2_5": "Ignored other agent's input",
    "FM_2_6": "Reasoning-action mismatch",
    # 3. Task verification and termination
    "FM_3_1": "Premature termination",
    "FM_3_2": "No or incomplete verification",
    "FM_3_3": "Incorrect verification",
}

INFRA_FAULTS = {
    "INF_LATENCY": "Add fixed or random latency before responding (e.g. 2-5 s)",
    "INF_UNAVAILABLE": "Return gRPC UNAVAILABLE / HTTP 503 for a share of calls",
    "INF_TIMEOUT": "Hang past the caller's deadline (DEADLINE_EXCEEDED)",
    "INF_LLM_DOWN": "LLM backend (Ollama) unreachable or slow",
    "INF_MALFORMED": "Return a syntactically valid but schema-breaking response",
}

# Signals the recommender looks for in each agent's /ui/results rows
EVIDENCE_KEYS = ("steps_lost", "infection_point", "boundary_alert_point")
