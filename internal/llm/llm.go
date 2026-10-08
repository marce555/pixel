package llm

import "context"

// Role defines the role of a message sender.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message represents a single message in a conversation.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Provider is the interface that any LLM engine (OpenAI, Ollama, etc.) must implement.
type Provider interface {
	// Generate takes a context and a list of messages (history) and returns the assistant's response.
	Generate(ctx context.Context, messages []Message) (string, error)

	// GenerateStream returns channels to stream the response as it is being generated.
	GenerateStream(ctx context.Context, messages []Message) (<-chan string, <-chan error)

	// CreateEmbedding creates a vector representation of a text string (useful for LTM / RAG).
	CreateEmbedding(ctx context.Context, text string) ([]float32, error)
}

// MaxTokensProvider is an optional interface implemented by providers that support specifying max generated tokens.
type MaxTokensProvider interface {
	GenerateWithMaxTokens(ctx context.Context, messages []Message, maxTokens int) (string, error)
}

// GenerationResult represents the outcome of an LLM generation with metadata.
type GenerationResult struct {
	Content      string `json:"content"`
	FinishReason string `json:"finish_reason"` // "stop", "length", etc.
}

// DetailedProvider is an optional interface implemented by providers that support returning generation metadata.
type DetailedProvider interface {
	GenerateDetailed(ctx context.Context, messages []Message, maxTokens int) (GenerationResult, error)
}

// GenerateWithTokens generates a completion using maxTokens if the provider implements MaxTokensProvider,
// or falls back to standard Generate.
func GenerateWithTokens(ctx context.Context, provider Provider, messages []Message, maxTokens int) (string, error) {
	if mtp, ok := provider.(MaxTokensProvider); ok {
		return mtp.GenerateWithMaxTokens(ctx, messages, maxTokens)
	}
	return provider.Generate(ctx, messages)
}

// GenerateDetailedWithFallback calls GenerateDetailed if supported, or falls back to GenerateWithTokens.
func GenerateDetailedWithFallback(ctx context.Context, provider Provider, messages []Message, maxTokens int) (GenerationResult, error) {
	if dp, ok := provider.(DetailedProvider); ok {
		return dp.GenerateDetailed(ctx, messages, maxTokens)
	}
	content, err := GenerateWithTokens(ctx, provider, messages, maxTokens)
	return GenerationResult{Content: content, FinishReason: "stop"}, err
}

// GenerateWithContinuation generates a completion. If finish_reason indicates token exhaustion ("length"),
// it triggers an automatic continuation loop (up to 2 passes) to seamlessly complete the output and conclusion.
// It also invokes onLengthDetected callback if provided, allowing callers to log an alert.
func GenerateWithContinuation(ctx context.Context, provider Provider, messages []Message, maxTokens int, onLengthDetected func(pass int)) (string, error) {
	res, err := GenerateDetailedWithFallback(ctx, provider, messages, maxTokens)
	if err != nil {
		return "", err
	}

	content := res.Content
	finishReason := res.FinishReason

	// If finish_reason indicates length truncation, execute automatic continuation loop
	pass := 0
	for (finishReason == "length" || finishReason == "max_tokens") && pass < 2 {
		pass++
		if onLengthDetected != nil {
			onLengthDetected(pass)
		}

		continuationMessages := append([]Message{}, messages...)
		continuationMessages = append(continuationMessages,
			Message{Role: RoleAssistant, Content: content},
			Message{Role: RoleUser, Content: "Continue immédiatement là où ton texte a été coupé pour achever complètement l'article et poser la conclusion. Ferme proprement toutes les balises HTML sans répéter ce qui précède."},
		)

		nextRes, nextErr := GenerateDetailedWithFallback(ctx, provider, continuationMessages, maxTokens)
		if nextErr != nil {
			break
		}

		content = content + "\n" + nextRes.Content
		finishReason = nextRes.FinishReason
	}

	return content, nil
}
