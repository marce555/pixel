package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompatibleProvider is designed to work with llama-server, LM Studio, Ollama, or OpenAI.
type OpenAICompatibleProvider struct {
	BaseURL        string
	APIKey         string
	ChatModel      string
	EmbeddingModel string
	Client         *http.Client
}

func NewOpenAICompatibleProvider(baseURL, apiKey, chatModel, embeddingModel string) *OpenAICompatibleProvider {
	return &OpenAICompatibleProvider{
		BaseURL:        baseURL,
		APIKey:         apiKey,
		ChatModel:      chatModel,
		EmbeddingModel: embeddingModel,
		Client: &http.Client{
			Timeout: 900 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true, // JSON payloads are not compressible
			},
		},
	}
}

type chatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

func parseAPIError(statusCode int, bodyBytes []byte) error {
	bodyStr := strings.TrimSpace(string(bodyBytes))
	if bodyStr == "" {
		return fmt.Errorf("API error %d", statusCode)
	}

	var errObj struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Type       string `json:"@type"`
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}

	if jsonErr := json.Unmarshal(bodyBytes, &errObj); jsonErr == nil && errObj.Error.Message != "" {
		msg := errObj.Error.Message
		msg = strings.ReplaceAll(msg, "\n", " ")
		msg = strings.ReplaceAll(msg, "\r", "")

		retryDelay := ""
		for _, det := range errObj.Error.Details {
			if det.RetryDelay != "" {
				retryDelay = det.RetryDelay
				break
			}
		}

		if retryDelay != "" {
			return fmt.Errorf("API error %d (%s: %s, retry in %s)", statusCode, errObj.Error.Status, msg, retryDelay)
		}
		if errObj.Error.Status != "" {
			return fmt.Errorf("API error %d (%s: %s)", statusCode, errObj.Error.Status, msg)
		}
		return fmt.Errorf("API error %d: %s", statusCode, msg)
	}

	bodyClean := strings.ReplaceAll(bodyStr, "\n", " ")
	bodyClean = strings.ReplaceAll(bodyClean, "\r", "")
	if len(bodyClean) > 200 {
		bodyClean = bodyClean[:200] + "..."
	}
	return fmt.Errorf("API error %d: %s", statusCode, bodyClean)
}

func (p *OpenAICompatibleProvider) Generate(ctx context.Context, messages []Message) (string, error) {
	return p.GenerateWithMaxTokens(ctx, messages, 2048)
}

func (p *OpenAICompatibleProvider) GenerateWithMaxTokens(ctx context.Context, messages []Message, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	reqBody := chatRequest{
		Model:     p.ChatModel,
		Messages:  messages,
		MaxTokens: maxTokens,
	}
	
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	var resp *http.Response
	for attempt := 1; attempt <= 8; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		baseURL := strings.TrimSuffix(p.BaseURL, "/")
		req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewBuffer(jsonData))
		if err != nil {
			return "", err
		}

		req.Header.Set("Content-Type", "application/json")
		if p.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.APIKey)
		}

		resp, err = p.Client.Do(req)
		if err == nil {
			break
		}

		if attempt == 8 {
			return "", fmt.Errorf("after 8 attempts, failed to contact LLM: %w", err)
		}

		fmt.Printf("[LLM Warning] Connection error (attempt %d/8): %v. Retrying in 1.5s...\n", attempt, err)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", parseAPIError(resp.StatusCode, bodyBytes)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var chatResp chatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return "", err
	}

	if len(chatResp.Choices) > 0 {
		return chatResp.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("no choices returned. Raw response: %s", parseAPIError(resp.StatusCode, bodyBytes))
}

func (p *OpenAICompatibleProvider) GenerateStream(ctx context.Context, messages []Message) (<-chan string, <-chan error) {
	out := make(chan string)
	errs := make(chan error, 1)

	go func() {
		defer close(out)
		defer close(errs)

		reqBody := chatRequest{
			Model:     p.ChatModel,
			Messages:  messages,
			Stream:    true,
			MaxTokens: 4096,
		}

		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			errs <- err
			return
		}

		var resp *http.Response
		for attempt := 1; attempt <= 8; attempt++ {
			baseURL := strings.TrimSuffix(p.BaseURL, "/")
			req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewBuffer(jsonData))
			if err != nil {
				errs <- err
				return
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			if p.APIKey != "" {
				req.Header.Set("Authorization", "Bearer "+p.APIKey)
			}

			resp, err = p.Client.Do(req)
			if err == nil {
				break
			}

			if attempt == 8 {
				errs <- fmt.Errorf("after 8 attempts, failed to start stream: %w", err)
				return
			}

			fmt.Printf("[LLM Warning] Connection error in stream (attempt %d/8): %v. Retrying in 1.5s...\n", attempt, err)
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			case <-time.After(1500 * time.Millisecond):
			}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			errs <- parseAPIError(resp.StatusCode, bodyBytes)
			return
		}

		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					errs <- err
				}
				break
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					break
				}
				var streamResp struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
					continue
				}
				if len(streamResp.Choices) > 0 && streamResp.Choices[0].Delta.Content != "" {
					out <- streamResp.Choices[0].Delta.Content
				}
			}
		}
	}()

	return out, errs
}

type embeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (p *OpenAICompatibleProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	reqBody := embeddingRequest{
		Model: p.EmbeddingModel,
		Input: text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	var resp *http.Response
	for attempt := 1; attempt <= 8; attempt++ {
		baseURL := strings.TrimSuffix(p.BaseURL, "/")
		// NOTE: L'endpoint standard est "/embeddings" (ou "/v1/embeddings" selon la BaseURL)
		req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/embeddings", bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")
		if p.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.APIKey)
		}

		resp, err = p.Client.Do(req)
		if err == nil {
			break
		}

		if attempt == 8 {
			return nil, fmt.Errorf("after 8 attempts, failed to create embedding: %w", err)
		}

		fmt.Printf("[LLM Warning] Connection error in embedding (attempt %d/8): %v. Retrying in 1.5s...\n", attempt, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		// On retourne un embedding vide plutôt qu'une erreur bloquante si le serveur ne supporte pas les embeddings
		fmt.Printf("[LLM Warning] Le serveur n'a pas pu créer l'embedding : %s\n", string(bodyBytes))
		return []float32{}, nil
	}

	var embResp embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, err
	}

	if len(embResp.Data) > 0 {
		return embResp.Data[0].Embedding, nil
	}

	return []float32{}, nil
}
