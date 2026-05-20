package main

import (
	"context"
	"fmt"

	"google.golang.org/genai"
)

func generateMusicWithGoogleSearch(ctx context.Context, apiKey, model, systemPrompt, query string, temperature float64) (string, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return "", fmt.Errorf("create genai client: %w", err)
	}

	temp := float32(temperature)
	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: systemPrompt}},
		},
		Temperature: &temp,
		Tools: []*genai.Tool{
			{GoogleSearch: &genai.GoogleSearch{}},
		},
	}

	contents := []*genai.Content{
		{Parts: []*genai.Part{{Text: query}}},
	}

	resp, err := client.Models.GenerateContent(ctx, model, contents, config)
	if err != nil {
		return "", fmt.Errorf("generate content: %w", err)
	}

	text := resp.Text()
	if text == "" {
		return "", fmt.Errorf("empty response from model")
	}

	return text, nil
}
