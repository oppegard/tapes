// Package openai
package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/papercomputeco/tapes/pkg/llm"
)

// Provider implements the Provider interface for OpenAI's Chat Completions API.
type Provider struct{}

func New() *Provider { return &Provider{} }

func (o *Provider) Name() string {
	return "openai"
}

// DefaultStreaming is false - OpenAI requires explicit "stream": true.
func (o *Provider) DefaultStreaming() bool {
	return false
}

func (o *Provider) ParseRequest(payload []byte) (*llm.ChatRequest, error) {
	normalized := normalizeJSONPayload(payload)

	var req openaiRequest
	if err := json.Unmarshal(normalized, &req); err != nil {
		return nil, err
	}

	messages := make([]llm.Message, 0, len(req.Messages))
	for _, msg := range req.Messages {
		converted := llm.Message{Role: msg.Role}

		switch content := msg.Content.(type) {
		case string:
			converted.Content = []llm.ContentBlock{{Type: "text", Text: content}}
		case []any:
			// Multimodal content (e.g., vision)
			for _, item := range content {
				if part, ok := item.(map[string]any); ok {
					cb := llm.ContentBlock{}
					if t, ok := part["type"].(string); ok {
						cb.Type = t
					}
					if text, ok := part["text"].(string); ok {
						cb.Text = text
					}
					if imageURL, ok := part["image_url"].(map[string]any); ok {
						cb.Type = "image"
						if url, ok := imageURL["url"].(string); ok {
							cb.ImageURL = url
						}
					}
					converted.Content = append(converted.Content, cb)
				}
			}
		case nil:
			// Empty content (can happen with tool calls)
			converted.Content = []llm.ContentBlock{}
		}

		// Handle tool calls in assistant messages
		for _, tc := range msg.ToolCalls {
			var input map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err == nil {
				converted.Content = append(converted.Content, llm.ContentBlock{
					Type:      "tool_use",
					ToolUseID: tc.ID,
					ToolName:  tc.Function.Name,
					ToolInput: input,
				})
			}
		}

		// Handle tool results
		if msg.Role == "tool" && msg.ToolCallID != "" {
			text := ""
			if s, ok := msg.Content.(string); ok {
				text = s
			}
			converted.Content = []llm.ContentBlock{{
				Type:         "tool_result",
				ToolResultID: msg.ToolCallID,
				ToolOutput:   text,
			}}
		}

		messages = append(messages, converted)
	}

	// Responses API uses "input" instead of "messages".
	if len(messages) == 0 {
		messages = parseResponsesInput(req.Input)
	}

	if req.Instructions != "" {
		messages = append([]llm.Message{llm.NewTextMessage("system", req.Instructions)}, messages...)
	}

	// Parse stop sequences
	var stop []string
	switch s := req.Stop.(type) {
	case string:
		stop = []string{s}
	case []any:
		for _, item := range s {
			if str, ok := item.(string); ok {
				stop = append(stop, str)
			}
		}
	}

	result := &llm.ChatRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        stop,
		Seed:        req.Seed,
		Stream:      req.Stream,
		RawRequest:  normalized,
	}

	// Preserve OpenAI-specific fields
	if req.FrequencyPenalty != nil || req.PresencePenalty != nil || req.ResponseFormat != nil {
		result.Extra = make(map[string]any)
		if req.FrequencyPenalty != nil {
			result.Extra["frequency_penalty"] = *req.FrequencyPenalty
		}
		if req.PresencePenalty != nil {
			result.Extra["presence_penalty"] = *req.PresencePenalty
		}
		if req.ResponseFormat != nil {
			result.Extra["response_format"] = req.ResponseFormat
		}
	}

	return result, nil
}

func (o *Provider) ParseResponse(payload []byte) (*llm.ChatResponse, error) {
	normalized := normalizeJSONPayload(payload)

	var resp openaiResponse
	if err := json.Unmarshal(normalized, &resp); err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		// Responses API uses output_text / output instead of choices.
		respMsg, stopReason := parseResponsesOutput(resp)
		if len(respMsg.Content) > 0 {
			return &llm.ChatResponse{
				Model:       resp.Model,
				Message:     respMsg,
				Done:        true,
				StopReason:  stopReason,
				Usage:       toUsage(resp.Usage),
				RawResponse: normalized,
			}, nil
		}

		// Return empty response if neither choices nor responses output are present.
		return &llm.ChatResponse{
			Model:       resp.Model,
			Done:        true,
			Usage:       toUsage(resp.Usage),
			RawResponse: normalized,
		}, nil
	}

	choice := resp.Choices[0]
	msg := choice.Message

	// Convert message content
	var content []llm.ContentBlock
	switch c := msg.Content.(type) {
	case string:
		content = []llm.ContentBlock{{Type: "text", Text: c}}
	case []any:
		for _, item := range c {
			if part, ok := item.(map[string]any); ok {
				cb := llm.ContentBlock{}
				if t, ok := part["type"].(string); ok {
					cb.Type = t
				}
				if text, ok := part["text"].(string); ok {
					cb.Text = text
				}
				content = append(content, cb)
			}
		}
	case nil:
		content = []llm.ContentBlock{}
	}

	// Handle tool calls
	for _, tc := range msg.ToolCalls {
		var input map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err == nil {
			content = append(content, llm.ContentBlock{
				Type:      "tool_use",
				ToolUseID: tc.ID,
				ToolName:  tc.Function.Name,
				ToolInput: input,
			})
		}
	}

	result := &llm.ChatResponse{
		Model: resp.Model,
		Message: llm.Message{
			Role:    msg.Role,
			Content: content,
		},
		Done:        true,
		StopReason:  choice.FinishReason,
		Usage:       toUsage(resp.Usage),
		CreatedAt:   time.Unix(resp.Created, 0),
		RawResponse: normalized,
		Extra: map[string]any{
			"id":     resp.ID,
			"object": resp.Object,
		},
	}

	return result, nil
}

func (o *Provider) ParseStreamChunk(_ []byte) (*llm.StreamChunk, error) {
	panic("Not yet implemented")
}

func toUsage(usage *openaiUsage) *llm.Usage {
	if usage == nil {
		return nil
	}

	prompt := usage.PromptTokens
	if prompt == 0 {
		prompt = usage.InputTokens
	}
	completion := usage.CompletionTokens
	if completion == 0 {
		completion = usage.OutputTokens
	}
	total := usage.TotalTokens
	if total == 0 {
		total = prompt + completion
	}

	result := &llm.Usage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
	}
	if usage.PromptTokensDetails != nil {
		result.CacheReadInputTokens = usage.PromptTokensDetails.CachedTokens
	}
	return result
}

func parseResponsesInput(input any) []llm.Message {
	if input == nil {
		return nil
	}

	switch v := input.(type) {
	case string:
		return []llm.Message{llm.NewTextMessage("user", v)}
	case []any:
		var out []llm.Message
		for _, item := range v {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role, _ := part["role"].(string)
			if role == "" {
				role = "user"
			}

			switch content := part["content"].(type) {
			case string:
				out = append(out, llm.NewTextMessage(role, content))
			case []any:
				var blocks []llm.ContentBlock
				for _, c := range content {
					cm, ok := c.(map[string]any)
					if !ok {
						continue
					}
					typ, _ := cm["type"].(string)
					text, _ := cm["text"].(string)
					switch typ {
					case "input_text", "text", "output_text":
						blocks = append(blocks, llm.ContentBlock{Type: "text", Text: text})
					}
				}
				if len(blocks) > 0 {
					out = append(out, llm.Message{Role: role, Content: blocks})
				}
			}
		}
		return out
	default:
		return nil
	}
}

func parseResponsesOutput(resp openaiResponse) (llm.Message, string) {
	if txt := strings.TrimSpace(resp.OutputText); txt != "" {
		return llm.NewTextMessage("assistant", txt), ""
	}

	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		role := item.Role
		if role == "" {
			role = "assistant"
		}
		var blocks []llm.ContentBlock
		for _, c := range item.Content {
			switch c.Type {
			case "output_text", "text", "input_text":
				blocks = append(blocks, llm.ContentBlock{Type: "text", Text: c.Text})
			}
		}
		if len(blocks) > 0 {
			return llm.Message{Role: role, Content: blocks}, ""
		}
	}

	return llm.Message{}, ""
}

func normalizeJSONPayload(payload []byte) []byte {
	trimmed := bytes.TrimSpace(payload)
	if json.Valid(trimmed) {
		return trimmed
	}

	// Some clients send a JSON string whose contents are JSON.
	if decoded := decodeJSONStringPayload(trimmed); len(decoded) > 0 {
		return decoded
	}

	// Some clients wrap payloads with parentheses, optionally with
	// extra suffixes such as semicolons: (<json>);
	if inner := extractParenthesized(trimmed); len(inner) > 0 {
		if decoded := decodeJSONStringPayload(inner); len(decoded) > 0 {
			return decoded
		}
		if json.Valid(inner) {
			return inner
		}
		if candidate := extractJSONCandidate(inner, '{', '}'); json.Valid(candidate) {
			return candidate
		}
		if candidate := extractJSONCandidate(inner, '[', ']'); json.Valid(candidate) {
			return candidate
		}
	}

	if candidate := extractJSONCandidate(trimmed, '{', '}'); json.Valid(candidate) {
		return candidate
	}
	if candidate := extractJSONCandidate(trimmed, '[', ']'); json.Valid(candidate) {
		return candidate
	}

	return trimmed
}

func extractJSONCandidate(payload []byte, open, close byte) []byte {
	start := bytes.IndexByte(payload, open)
	end := bytes.LastIndexByte(payload, close)
	if start == -1 || end == -1 || end <= start {
		return nil
	}
	return bytes.TrimSpace(payload[start : end+1])
}

func extractParenthesized(payload []byte) []byte {
	start := bytes.IndexByte(payload, '(')
	end := bytes.LastIndexByte(payload, ')')
	if start == -1 || end == -1 || end <= start {
		return nil
	}
	return bytes.TrimSpace(payload[start+1 : end])
}

func decodeJSONStringPayload(payload []byte) []byte {
	var decoded string
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil
	}

	trimmed := bytes.TrimSpace([]byte(decoded))
	if json.Valid(trimmed) {
		return trimmed
	}
	if candidate := extractJSONCandidate(trimmed, '{', '}'); json.Valid(candidate) {
		return candidate
	}
	if candidate := extractJSONCandidate(trimmed, '[', ']'); json.Valid(candidate) {
		return candidate
	}
	return nil
}
