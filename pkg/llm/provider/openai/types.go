package openai

// openaiRequest represents OpenAI's request format.
type openaiRequest struct {
	Model        string          `json:"model"`
	Messages     []openaiMessage `json:"messages"`
	Input        any             `json:"input,omitempty"` // Responses API input
	Instructions string          `json:"instructions,omitempty"`
	MaxTokens    *int            `json:"max_tokens,omitempty"`
	Temperature  *float64        `json:"temperature,omitempty"`
	TopP         *float64        `json:"top_p,omitempty"`
	Stop         any             `json:"stop,omitempty"` // string or []string
	Seed         *int            `json:"seed,omitempty"`
	Stream       *bool           `json:"stream,omitempty"`
	// Additional OpenAI-specific fields
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64       `json:"presence_penalty,omitempty"`
	ResponseFormat   map[string]any `json:"response_format,omitempty"`
}

// openaiMessage represents a message in OpenAI's format.
type openaiMessage struct {
	Role       string `json:"role"`
	Content    any    `json:"content"` // string or []openaiContentPart for vision
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
}

// openaiResponse represents OpenAI's response format.
type openaiResponse struct {
	ID         string             `json:"id"`
	Object     string             `json:"object"`
	Created    int64              `json:"created"`
	Model      string             `json:"model"`
	OutputText string             `json:"output_text,omitempty"` // Responses API
	Output     []openaiOutputItem `json:"output,omitempty"`      // Responses API
	Choices    []struct {
		Index        int           `json:"index"`
		Message      openaiMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage *openaiUsage `json:"usage,omitempty"`
}

type openaiUsage struct {
	PromptTokens        int                        `json:"prompt_tokens"`
	CompletionTokens    int                        `json:"completion_tokens"`
	TotalTokens         int                        `json:"total_tokens"`
	InputTokens         int                        `json:"input_tokens"`  // Responses API
	OutputTokens        int                        `json:"output_tokens"` // Responses API
	PromptTokensDetails *openaiPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

type openaiPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type openaiOutputItem struct {
	Type    string             `json:"type"`
	Role    string             `json:"role,omitempty"`
	Content []openaiOutputPart `json:"content,omitempty"`
}

type openaiOutputPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
