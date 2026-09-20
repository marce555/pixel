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

// GenerateWithTokens generates a completion using maxTokens if the provider implements MaxTokensProvider,
// or falls back to standard Generate.
func GenerateWithTokens(ctx context.Context, provider Provider, messages []Message, maxTokens int) (string, error) {
	if mtp, ok := provider.(MaxTokensProvider); ok {
		return mtp.GenerateWithMaxTokens(ctx, messages, maxTokens)
	}
	return provider.Generate(ctx, messages)
}

