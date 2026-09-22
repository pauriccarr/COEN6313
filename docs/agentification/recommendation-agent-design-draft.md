# Recommendation Agent Design

## 1. Current API

```text
ListRecommendationsRequest
├── user_id
└── product_ids
        ↓
     v1 logic
        ↓
ListRecommendationsResponse
└── product_ids
```

## 2. Current Logic

```text
Frontend
   │
   │ ListRecommendationsRequest
   ▼
Recommendation Service v1
   │
   ├── call Product Catalog
   │
   ▼
Get Products
   │
   ▼
Random Selection
   │
   ▼
ListRecommendationsResponse
```

## 3. Current Dependencies

```text
Recommendation Service
          │
          │ gRPC
          ▼
Product Catalog Service
```

## 4. Current Limitations / Opportunities for Agentification

### 1. Random recommendation

**Limitation:**

v1 randomly selects products rather than making an intelligent recommendation decision.

**Opportunity:**

Replace random selection with a model-driven recommendation decision.

### 2. Limited use of request context

**Limitation:**

Although the API provides user_id and product_ids, the current recommendation logic mainly uses product_ids for filtering and does not meaningfully, use user_id for recommendation decisions.

**Opportunity:**

Agent v2 can use richer context to make context-aware recommendations.

### 3. Limited use of product information

**Limitation:**

v1 retrieves products from Product Catalog, but the recommendation decision is still based on random selection rather than product information.

**Opportunity:**

Agent v2 can use Product Catalog as a backend/tool to obtain richer product information and use it in the recommendation decision.

## 5. Agent v2 workflow

```text
                 Original gRPC Request
                          ↓
                 Recommendation Agent
                          ↓
             ┌────────────┴────────────┐
             ↓                         ↓
      Request Context          Product Information
             │                         │
             └────────────┬────────────┘
                          ↓
                 Build Agent Context
                          ↓
                     LLM / Model
                          ↓
             Model-driven Recommendation
                          ↓
                   Validate Result
                          ↓
                Original gRPC Response
```

## 6. Model / Runtime / Tool Selection

The final model and runtime have not been selected yet. Candidate solutions will be evaluated before the final implementation decision.

### 6.1 Model Candidates

Current candidates include:

- **Gemini**
- **Other LLM candidate(s)**

The final model will be selected after comparison and testing.

### 6.2 Model Evaluation Criteria

Candidate models will be evaluated based on:

- **Structured Output Reliability**
  - Whether the model can reliably return recommendation results in the required structured format.

- **Function / Tool Calling Capability**
  - Whether the model can reliably decide when and how to call external tools.

- **Latency**
  - Response time during recommendation requests.

- **Cost**
  - Model/API usage cost.

- **Integration Complexity**
  - Difficulty of integrating the model into the existing Recommendation Service architecture.

### 6.3 Model Access / Runtime Options

Two model access approaches are currently being considered:

- **API-based**
  - Gemini API

- **Self-hosted**
  - Ollama with a locally/self-hosted model

The API-based and self-hosted approaches will be compared before the final selection.

### 6.4 Agent Tool

#### Product Catalog Tool

The existing Product Catalog Service can be exposed to the Agent as a callable tool.

The Agent should be able to:

- Decide whether additional product information is needed.
- Call the Product Catalog Tool when necessary.
- Use the returned product information to continue the recommendation process.

### 6.5 Target Agent Workflow

The intended design gives the Agent more control over both the recommendation decision and the use of external product information.

```text
Agent / Model
     ↓
Do I need more product information?
     │
     ├── No
     │    ↓
     │  Make recommendation decision
     │
     └── Yes
          ↓
     Call Product Catalog Tool
          ↓
     Get product information
          ↓
     Continue recommendation process
          ↓
     Make recommendation decision