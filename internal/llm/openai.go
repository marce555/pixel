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
	BaseURL         string
	FallbackBaseURL string
	FallbackModel   string
	APIKey          string
	ChatModel       string
	EmbeddingModel  string
	Temperature     *float64
	Client          *http.Client
}

func NewOpenAICompatibleProvider(baseURL, apiKey, chatModel, embeddingModel string) *OpenAICompatibleProvider {
	defaultTemp := 0.2
	return &OpenAICompatibleProvider{
		BaseURL:        baseURL,
		APIKey:         apiKey,
		ChatModel:      chatModel,
		EmbeddingModel: embeddingModel,
		Temperature:    &defaultTemp,
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

func (p *OpenAICompatibleProvider) SetFallback(fallbackBaseURL, fallbackModel string) {
	p.FallbackBaseURL = fallbackBaseURL
	p.FallbackModel = fallbackModel
}

func (p *OpenAICompatibleProvider) SetTemperature(temp float64) {
	p.Temperature = &temp
}

type apiChatMessage struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model               string           `json:"model"`
	Messages            []apiChatMessage `json:"messages"`
	Stream              bool             `json:"stream,omitempty"`
	MaxTokens           int              `json:"max_tokens,omitempty"`
	MaxCompletionTokens int              `json:"max_completion_tokens,omitempty"`
	Temperature         *float64         `json:"temperature,omitempty"`
}

func DefaultOneMinModels() []string {
	return []string{
		"gpt-4.1-mini",
		"gpt-4.1",
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-5",
		"gpt-5-mini",
		"gpt-5-nano",
		"gpt-5.1",
		"gpt-5.2",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.4-nano",
		"gpt-5.5",
		"gpt-5.6-luna",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-6-astra",
		"gpt-6-luna",
		"gpt-6-sol",
		"o3",
		"deepseek-v4-pro",
		"deepseek-flash",
		"moonshotai/kimi-k2.7-code",
		"moonshotai/kimi-k2.6",
		"qwen3.7-max",
		"qwen3.7-plus",
		"qwen3.7-flash",
		"qwen3.6-plus",
		"qwen3.6-flash",
		"qwen-max",
		"qwen-plus",
		"qwen-flash",
		"mistral-large-latest",
		"mistral-large-2512",
		"mistral-medium-3-5",
		"mistral-medium-latest",
		"mistral-small-2603",
		"mistral-small-latest",
		"open-mistral-nemo",
		"ministral-14b-latest",
		"magistral-medium-latest",
		"magistral-small-latest",
		"meta/muse-spark-1.3",
		"meta/muse-spark-1.2",
		"meta/muse-spark-1.1",
		"glm-5.3",
		"glm-5.2",
		"glm-5.1",
		"glm-5",
	}
}

func FetchRemoteModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.1min.ai/openai/v1"
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, parseAPIError(resp.StatusCode, body)
	}

	var listResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, err
	}
	var models []string
	seen := make(map[string]bool)
	for _, m := range listResp.Data {
		id := strings.TrimSpace(m.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	return models, nil
}

func toAPIMessages(messages []Message) []apiChatMessage {
	apiMsgs := make([]apiChatMessage, len(messages))
	for i, m := range messages {
		apiMsgs[i] = apiChatMessage{Role: m.Role, Content: m.Content}
	}
	return apiMsgs
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
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
	return p.GenerateWithMaxTokens(ctx, messages, 4096)
}

func (p *OpenAICompatibleProvider) GenerateWithMaxTokens(ctx context.Context, messages []Message, maxTokens int) (string, error) {
	res, err := p.GenerateDetailed(ctx, messages, maxTokens)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// GenerateDetailed implements DetailedProvider interface to expose FinishReason.
func (p *OpenAICompatibleProvider) GenerateDetailed(ctx context.Context, messages []Message, maxTokens int) (GenerationResult, error) {
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	isOneMin := strings.Contains(strings.ToLower(p.BaseURL), "1min.ai")
	modelLower := strings.ToLower(strings.TrimSpace(p.ChatModel))
	useMaxCompletion := isOneMin || strings.HasPrefix(modelLower, "gpt-4.1") || strings.HasPrefix(modelLower, "gpt-5") || strings.HasPrefix(modelLower, "o1") || strings.HasPrefix(modelLower, "o3") || strings.HasPrefix(modelLower, "o4")
	omitTemp := strings.HasPrefix(modelLower, "o1") || strings.HasPrefix(modelLower, "o3") || strings.HasPrefix(modelLower, "o4") ||
		(isOneMin && (strings.Contains(modelLower, "deepseek") || strings.Contains(modelLower, "flash") || strings.Contains(modelLower, "reasoner") || strings.Contains(modelLower, "thinking")))

	reqBody := chatRequest{
		Model:       p.ChatModel,
		Messages:    toAPIMessages(messages),
		Temperature: p.Temperature,
	}
	if omitTemp {
		reqBody.Temperature = nil
	}
	if useMaxCompletion {
		effectiveTokens := maxTokens
		if isOneMin && strings.Contains(modelLower, "flash") && effectiveTokens > 1024 {
			effectiveTokens = 1024
		} else if isOneMin && effectiveTokens > 2048 {
			effectiveTokens = 2048
		}
		reqBody.MaxCompletionTokens = effectiveTokens
	} else {
		reqBody.MaxTokens = maxTokens
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return GenerationResult{}, err
	}

	maxAttempts := 8
	if isOneMin {
		// Respect 1min.AI max_retries=0 recommendation (plus 1 fallback attempt if FallbackBaseURL is configured)
		if p.FallbackBaseURL != "" {
			maxAttempts = 2
		} else {
			maxAttempts = 1
		}
	}

	var resp *http.Response
	usedFallback := false
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return GenerationResult{}, ctxErr
		}
		baseURL := strings.TrimSuffix(p.BaseURL, "/")
		payload := jsonData
		apiKey := p.APIKey
		if attempt > 1 && p.FallbackBaseURL != "" {
			usedFallback = true
			baseURL = strings.TrimSuffix(p.FallbackBaseURL, "/")
			if strings.Contains(baseURL, "127.0.0.1") || strings.Contains(baseURL, "localhost") {
				apiKey = ""
			}
			fbReq := chatRequest{
				Model:       p.ChatModel,
				Messages:    toAPIMessages(messages),
				MaxTokens:   maxTokens,
				Temperature: p.Temperature,
			}
			if p.FallbackModel != "" {
				fbReq.Model = p.FallbackModel
			}
			if fbData, errM := json.Marshal(fbReq); errM == nil {
				payload = fbData
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewBuffer(payload))
		if err != nil {
			return GenerationResult{}, err
		}

		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err = p.Client.Do(req)
		if err == nil {
			break
		}

		if attempt == maxAttempts {
			return GenerationResult{}, fmt.Errorf("failed to contact LLM (%s): %w", baseURL, err)
		}

		if attempt == 1 && p.FallbackBaseURL != "" {
			fmt.Printf("[LLM Fallback] Fournisseur (%s) indisponible (%v), basculement immédiat sur %s...\n", p.BaseURL, err, p.FallbackBaseURL)
			continue
		}

		fmt.Printf("[LLM Warning] Connection error (attempt %d/%d): %v. Retrying in 1.5s...\n", attempt, maxAttempts, err)
		select {
		case <-ctx.Done():
			return GenerationResult{}, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		errBodyStr := strings.ToLower(string(bodyBytes))
		// Compatibilité automatique max_completion_tokens <-> max_tokens si un modèle spécifique sur 1min.AI ou OpenAI exige l'autre champ
		if resp.StatusCode == http.StatusBadRequest && !usedFallback && (strings.Contains(errBodyStr, "max_tokens") || strings.Contains(errBodyStr, "max_completion_tokens") || strings.Contains(errBodyStr, "temperature")) {
			altReq := chatRequest{
				Model:    p.ChatModel,
				Messages: toAPIMessages(messages),
			}
			if strings.Contains(errBodyStr, "temperature") {
				altReq.Temperature = nil
				if reqBody.MaxCompletionTokens > 0 {
					altReq.MaxCompletionTokens = reqBody.MaxCompletionTokens
				} else {
					altReq.MaxTokens = reqBody.MaxTokens
				}
			} else {
				if !omitTemp {
					altReq.Temperature = p.Temperature
				}
				if reqBody.MaxCompletionTokens > 0 {
					altReq.MaxTokens = maxTokens
				} else {
					altReq.MaxCompletionTokens = maxTokens
				}
			}
			if altData, errM := json.Marshal(altReq); errM == nil {
				baseURL := strings.TrimSuffix(p.BaseURL, "/")
				if req2, errR := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewBuffer(altData)); errR == nil {
					req2.Header.Set("Content-Type", "application/json")
					if p.APIKey != "" {
						req2.Header.Set("Authorization", "Bearer "+p.APIKey)
					}
					if resp2, errDo := p.Client.Do(req2); errDo == nil {
						defer resp2.Body.Close()
						if resp2.StatusCode == http.StatusOK {
							bodyBytes2, _ := io.ReadAll(resp2.Body)
							var chatResp2 chatResponse
							if json.Unmarshal(bodyBytes2, &chatResp2) == nil && len(chatResp2.Choices) > 0 {
								return GenerationResult{
									Content:      chatResp2.Choices[0].Message.Content,
									FinishReason: chatResp2.Choices[0].FinishReason,
								}, nil
							}
						}
					}
				}
			}
		}
		// Si un serveur de repli local est configuré et que l'API Cloud a renvoyé une erreur HTTP (quota, clé, 5xx), basculer sur le modèle local
		if !usedFallback && p.FallbackBaseURL != "" {
			fbURL := strings.TrimSuffix(p.FallbackBaseURL, "/")
			fbModel := p.FallbackModel
			if fbModel == "" {
				fbModel = "qwen3.5:9b"
			}
			fmt.Printf("[LLM Fallback] API (%s / %s) a renvoyé HTTP %d, repli transparent sur %s (%s)...\n", p.BaseURL, p.ChatModel, resp.StatusCode, fbURL, fbModel)
			fbProvider := NewOpenAICompatibleProvider(fbURL, "", fbModel, "")
			return fbProvider.GenerateDetailed(ctx, messages, maxTokens)
		}
		return GenerationResult{}, parseAPIError(resp.StatusCode, bodyBytes)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return GenerationResult{}, err
	}

	var chatResp chatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return GenerationResult{}, err
	}

	if len(chatResp.Choices) > 0 {
		return GenerationResult{
			Content:      chatResp.Choices[0].Message.Content,
			FinishReason: chatResp.Choices[0].FinishReason,
		}, nil
	}

	return GenerationResult{}, fmt.Errorf("no choices returned. Raw response: %s", parseAPIError(resp.StatusCode, bodyBytes))
}

func (p *OpenAICompatibleProvider) GenerateStream(ctx context.Context, messages []Message) (<-chan string, <-chan error) {
	out := make(chan string)
	errs := make(chan error, 1)

	go func() {
		defer close(out)
		defer close(errs)

		isOneMin := strings.Contains(strings.ToLower(p.BaseURL), "1min.ai")
		modelLower := strings.ToLower(strings.TrimSpace(p.ChatModel))
		useMaxCompletion := isOneMin || strings.HasPrefix(modelLower, "gpt-4.1") || strings.HasPrefix(modelLower, "gpt-5") || strings.HasPrefix(modelLower, "o1") || strings.HasPrefix(modelLower, "o3") || strings.HasPrefix(modelLower, "o4")
		omitTemp := strings.HasPrefix(modelLower, "o1") || strings.HasPrefix(modelLower, "o3") || strings.HasPrefix(modelLower, "o4") ||
			(isOneMin && (strings.Contains(modelLower, "deepseek") || strings.Contains(modelLower, "flash") || strings.Contains(modelLower, "reasoner") || strings.Contains(modelLower, "thinking")))

		reqBody := chatRequest{
			Model:       p.ChatModel,
			Messages:    toAPIMessages(messages),
			Stream:      true,
			Temperature: p.Temperature,
		}
		if omitTemp {
			reqBody.Temperature = nil
		}
		if useMaxCompletion {
			if isOneMin && strings.Contains(modelLower, "flash") {
				reqBody.MaxCompletionTokens = 1024
			} else if isOneMin {
				reqBody.MaxCompletionTokens = 2048
			} else {
				reqBody.MaxCompletionTokens = 4096
			}
		} else {
			reqBody.MaxTokens = 4096
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
			errBodyStr := strings.ToLower(string(bodyBytes))
			if resp.StatusCode == http.StatusBadRequest && (strings.Contains(errBodyStr, "temperature") || strings.Contains(errBodyStr, "max_tokens") || strings.Contains(errBodyStr, "max_completion_tokens")) {
				altReq := chatRequest{
					Model:    p.ChatModel,
					Messages: toAPIMessages(messages),
					Stream:   true,
				}
				if strings.Contains(errBodyStr, "temperature") {
					altReq.Temperature = nil
					if reqBody.MaxCompletionTokens > 0 {
						altReq.MaxCompletionTokens = reqBody.MaxCompletionTokens
					} else {
						altReq.MaxTokens = reqBody.MaxTokens
					}
				} else {
					if !omitTemp {
						altReq.Temperature = p.Temperature
					}
					if reqBody.MaxCompletionTokens > 0 {
						altReq.MaxTokens = 4096
					} else {
						altReq.MaxCompletionTokens = 4096
					}
				}
				if altData, errM := json.Marshal(altReq); errM == nil {
					baseURL := strings.TrimSuffix(p.BaseURL, "/")
					if req2, errR := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewBuffer(altData)); errR == nil {
						req2.Header.Set("Content-Type", "application/json")
						req2.Header.Set("Accept", "text/event-stream")
						if p.APIKey != "" {
							req2.Header.Set("Authorization", "Bearer "+p.APIKey)
						}
						if resp2, errDo := p.Client.Do(req2); errDo == nil && resp2.StatusCode == http.StatusOK {
							resp = resp2
							defer resp.Body.Close()
							goto streamReader
						}
					}
				}
			}
			errs <- parseAPIError(resp.StatusCode, bodyBytes)
			return
		}

streamReader:
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
		if strings.Contains(baseURL, "52625") {
			baseURL = strings.Replace(baseURL, "52625", "52626", 1)
		} else if strings.Contains(baseURL, "18081") {
			baseURL = strings.Replace(baseURL, "18081", "52626", 1)
		} else if strings.Contains(baseURL, "18082") {
			baseURL = strings.Replace(baseURL, "18082", "52626", 1)
		}
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
