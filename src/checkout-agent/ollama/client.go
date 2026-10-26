// Package ollama provides a minimal client for the Ollama HTTP API,
// implementing the tool-use (function-calling) loop used by the checkout agent.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// ToolDefinition describes a tool the model can call.
type ToolDefinition struct {
	Type     string       `json:"type"` // always "function"
	Function FunctionMeta `json:"function"`
}

// FunctionMeta holds the name/description/parameters schema for a tool.
type FunctionMeta struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Message is a single chat turn.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// For role=tool responses back to the model.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall is emitted by the model when it wants to invoke a tool.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall carries the tool name and JSON-encoded arguments.
//type FunctionCall struct {
//	Name      string `json:"name"`
//	Arguments string `json:"arguments"`
//}

type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // ? accepts both string and object
}

// chatRequest is the body sent to /api/chat.
type chatRequest struct {
	Model    string           `json:"model"`
	Messages []Message        `json:"messages"`
	Tools    []ToolDefinition `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
}

// chatResponse is the body received from /api/chat.
type chatResponse struct {
	Message    Message `json:"message"`
	Done       bool    `json:"done"`
	DoneReason string  `json:"done_reason,omitempty"`

	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

// Client talks to a running Ollama instance.
type Client struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

// New creates a new Ollama client.
func New(baseURL, model string) *Client {
	return &Client{
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 360 * time.Second,
		},
	}
}

// Chat sends messages to the model and returns the assistant reply.
// It does NOT run the tool loop  that lives in the agent.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Message, error) {
	body := chatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    tools,
		Stream:   false,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}

	// DEBUG: Log the request size and start time
	startTime := time.Now()
	fmt.Printf("[Ollama Debug] Sending request to %s/api/chat | Model: %s | Messages: %d | Payload Size: %d bytes\n",
		c.baseURL, c.model, len(messages), len(data))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// DEBUG: Capture how long it took before failing
		duration := time.Since(startTime)
		return nil, fmt.Errorf("ollama request failed after %v: %w", duration, err)
	}
	defer resp.Body.Close()

	duration := time.Since(startTime)
	fmt.Printf("[Ollama Debug] Received response in %v | Status: %s\n", duration, resp.Status)

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama non-200 status %d: %s", resp.StatusCode, string(b))
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decode chat response: %w", err)
	}
	inputTokens := cr.PromptEvalCount
	outputTokens := cr.EvalCount
	totalTokens := inputTokens + outputTokens

	fmt.Printf("TOKEN_METRICS input=%d output=%d total=%d\n", inputTokens, outputTokens, totalTokens)

	f, err := os.OpenFile("token_log.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		defer f.Close()
		fmt.Fprintf(f, "%d\n", totalTokens)
	}

	return &cr.Message, nil
}
