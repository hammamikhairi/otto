// Package gpt provides an OpenAI-compatible chat client used by the
// OttoCook AI agent to answer cooking questions and (eventually) modify
// recipe state via tool calls.
package gpt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hammamikhairi/ottocook/internal/logger"
)

// ── Wire types ───────────────────────────────────────────────────

// Role constants.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is a single chat-completion message.
type Message struct {
	Role    string    `json:"role"`
	Content []Content `json:"content"`
}

// TextMessage is a convenience constructor for a plain-text message.
func TextMessage(role, text string) Message {
	return Message{
		Role:    role,
		Content: []Content{{Type: "text", Text: text}},
	}
}

// Content is a polymorphic content block (text or image_url).
type Content struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL wraps an image reference.
type ImageURL struct {
	URL string `json:"url"`
}

// payload is the request body sent to the chat-completions endpoint.
type payload struct {
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	TopP        float64   `json:"top_p"`
	MaxTokens   int       `json:"max_tokens"`
	Model       string    `json:"model,omitempty"`
}

// apiResponse is the top-level response envelope.
type apiResponse struct {
	Choices []choice `json:"choices"`
}

type choice struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

// ── Client ───────────────────────────────────────────────────────

// ClientOption configures the Client.
type ClientOption func(*Client)

// WithModel overrides the default model name.
func WithModel(model string) ClientOption {
	return func(c *Client) { c.model = model }
}

// WithTemperature overrides the sampling temperature.
func WithTemperature(t float64) ClientOption {
	return func(c *Client) { c.temperature = t }
}

// WithMaxTokens sets the response token limit.
func WithMaxTokens(n int) ClientOption {
	return func(c *Client) { c.maxTokens = n }
}

// WithHTTPTimeout sets the HTTP client timeout.
func WithHTTPTimeout(d time.Duration) ClientOption {
	return func(c *Client) { c.http.Timeout = d }
}

// Client talks to an OpenAI-compatible chat-completions endpoint.
type Client struct {
	endpoint    string
	apiKey      string
	model       string
	temperature float64
	topP        float64
	maxTokens   int
	http        *http.Client
	log         *logger.Logger
}

// NewClient creates an OpenAI chat client.
//   - endpoint: full URL to the chat/completions resource
//     (e.g. "https://<resource>.openai.azure.com/openai/deployments/<dep>/chat/completions?api-version=2024-02-01")
//   - apiKey:   the subscription / API key
func NewClient(endpoint, apiKey string, log *logger.Logger, opts ...ClientOption) *Client {
	c := &Client{
		endpoint:    endpoint,
		apiKey:      apiKey,
		model:       "", // omitted for Azure deployments; set via WithModel for OpenAI
		temperature: 0.7,
		topP:        0.95,
		maxTokens:   800,
		http:        &http.Client{Timeout: 30 * time.Second},
		log:         log,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Chat sends a chat-completion request and returns the assistant's reply.
func (c *Client) Chat(ctx context.Context, messages []Message) (string, error) {
	body := payload{
		Messages:    messages,
		Temperature: c.temperature,
		TopP:        c.topP,
		MaxTokens:   c.maxTokens,
		Model:       c.model,
	}

	jsonData, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("gpt: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("gpt: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", c.apiKey)

	c.log.Debug("gpt: POST %s (%d bytes)", c.endpoint, len(jsonData))

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gpt: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("gpt: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gpt: API %s\n%s", resp.Status, string(respBody))
	}

	var result apiResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("gpt: unmarshal response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("gpt: empty response (no choices)")
	}

	reply := result.Choices[0].Message.Content
	c.log.Debug("gpt: reply (%d chars): %s", len(reply), truncate(reply, 120))
	return reply, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
