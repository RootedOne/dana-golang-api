package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Temperature float64         `json:"temperature,omitempty"`
	Messages    []openAIMessage `json:"messages"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// normalizeChatCompletionsURL accepts either a base (https://api.groq.com/openai/v1)
// or a full completions URL.
func normalizeChatCompletionsURL(base string) string {
	base = strings.TrimSpace(base)
	base = strings.TrimSuffix(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

func generateOpenAIChatCompletion(ctx context.Context, endpoint, apiKey, model, systemPrompt, query string, temperature float64) (string, error) {
	url := normalizeChatCompletionsURL(endpoint)

	payload := openAIChatRequest{
		Model:       model,
		Temperature: temperature,
		Messages: []openAIMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: query},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp openAIChatResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != nil && errResp.Error.Message != "" {
			return "", fmt.Errorf("API error %d: %s", resp.StatusCode, errResp.Error.Message)
		}
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}

	var chatResp openAIChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if chatResp.Error != nil && chatResp.Error.Message != "" {
		return "", fmt.Errorf("API error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("empty response from model")
	}

	return chatResp.Choices[0].Message.Content, nil
}
