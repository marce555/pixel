package llm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// AdaptiveProvider wraps a local and cloud provider, enabling dynamic toggling
// and automatic fallback to local in case of network cuts or cloud API issues.
type AdaptiveProvider struct {
	mu                 sync.RWMutex
	activeMode         string // "local", "cloud", "auto", "adaptive"
	localBaseURL       string
	localModel         string
	cloudBaseURL       string
	cloudModel         string
	cloudAPIKey        string
	cloudCooldownUntil time.Time
	localProvider      *OpenAICompatibleProvider
	cloudProvider      *OpenAICompatibleProvider
}

func resolveCloudAPIKey(cloudBaseURL, cloudAPIKey string) string {
	if strings.Contains(strings.ToLower(cloudBaseURL), "1min.ai") {
		if k := strings.TrimSpace(os.Getenv("ONEMIN_API_KEY")); k != "" && cloudAPIKey == "" {
			return k
		}
		if k := strings.TrimSpace(os.Getenv("KEY_1MIN")); k != "" && cloudAPIKey == "" {
			return k
		}
		if cloudAPIKey == "" {
			if data, err := os.ReadFile(".key_1min.txt"); err == nil && strings.TrimSpace(string(data)) != "" {
				return strings.TrimSpace(string(data))
			}
		}
		return strings.TrimSpace(cloudAPIKey)
	}
	if cloudAPIKey == "" {
		if k := os.Getenv("GEMINI_API_KEY"); k != "" {
			return k
		} else if data, err := os.ReadFile(".key_Gemini.txt"); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return cloudAPIKey
}

func NewAdaptiveProvider(activeMode, cloudAPIKey, cloudBaseURL, cloudModel, localBaseURL, localModel string) *AdaptiveProvider {
	cloudAPIKey = resolveCloudAPIKey(cloudBaseURL, cloudAPIKey)
	if strings.Contains(strings.ToLower(cloudBaseURL), "1min.ai") {
		if cloudModel == "" {
			cloudModel = "gpt-4.1-mini"
		}
	} else if cloudModel == "" || cloudModel == "gemini-3.6-flash" || cloudModel == "gemini-1.5-flash" {
		cloudModel = "gemini-3.5-flash"
	}
	p := &AdaptiveProvider{
		activeMode:   activeMode,
		cloudAPIKey:  cloudAPIKey,
		cloudBaseURL: cloudBaseURL,
		cloudModel:   cloudModel,
		localBaseURL: localBaseURL,
		localModel:   localModel,
	}
	localEmbed := "nomic-embed-text-v2-moe-GGUF"
	if strings.Contains(localBaseURL, "52625") || strings.Contains(localBaseURL, "18081") || strings.Contains(localBaseURL, "18082") {
		localEmbed = "embed-gemma:300m"
	}
	p.localProvider = NewOpenAICompatibleProvider(localBaseURL, "", localModel, localEmbed)
	p.cloudProvider = NewOpenAICompatibleProvider(cloudBaseURL, cloudAPIKey, cloudModel, "gemini-embedding-2")
	return p
}

func (p *AdaptiveProvider) SetLocalFallback(fallbackBaseURL, fallbackModel string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.localProvider != nil {
		p.localProvider.SetFallback(fallbackBaseURL, fallbackModel)
	}
}

func isLocalEndpoint(baseURL string) bool {
	u := strings.ToLower(strings.TrimSpace(baseURL))
	return u == "" || strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost") || strings.Contains(u, "0.0.0.0") || strings.Contains(u, "192.168.")
}

// NewTaskAdaptiveProvider creates an AdaptiveProvider tailored for a specific cognitive task
// (e.g., Publications, Review/Fact-Check, or Code/Skills), using cloud (1min.AI / Gemini) when a
// cloud URL is selected and automatically falling back to fallbackLocalURL/fallbackLocalModel.
func NewTaskAdaptiveProvider(targetBaseURL, targetModel, apiKey, fallbackLocalURL, fallbackLocalModel string) *AdaptiveProvider {
	if isLocalEndpoint(targetBaseURL) {
		localURL := targetBaseURL
		if localURL == "" {
			localURL = fallbackLocalURL
		}
		localMod := targetModel
		if localMod == "" {
			localMod = fallbackLocalModel
		}
		p := NewAdaptiveProvider("local", apiKey, "https://api.1min.ai/openai/v1", "gpt-4.1-mini", localURL, localMod)
		if localURL != fallbackLocalURL && fallbackLocalURL != "" {
			p.SetLocalFallback(fallbackLocalURL, fallbackLocalModel)
		}
		return p
	}
	return NewAdaptiveProvider("adaptive", apiKey, targetBaseURL, targetModel, fallbackLocalURL, fallbackLocalModel)
}

// UpdateTaskSettings dynamically updates a task-specific AdaptiveProvider when GUI settings change.
func (p *AdaptiveProvider) UpdateTaskSettings(targetBaseURL, targetModel, apiKey, fallbackLocalURL, fallbackLocalModel string) {
	if isLocalEndpoint(targetBaseURL) {
		localURL := targetBaseURL
		if localURL == "" {
			localURL = fallbackLocalURL
		}
		localMod := targetModel
		if localMod == "" {
			localMod = fallbackLocalModel
		}
		p.UpdateSettings("local", apiKey, "https://api.1min.ai/openai/v1", "gpt-4.1-mini", localURL, localMod)
		if localURL != fallbackLocalURL && fallbackLocalURL != "" {
			p.SetLocalFallback(fallbackLocalURL, fallbackLocalModel)
		}
		return
	}
	p.UpdateSettings("adaptive", apiKey, targetBaseURL, targetModel, fallbackLocalURL, fallbackLocalModel)
}

func (p *AdaptiveProvider) UpdateSettings(activeMode, cloudAPIKey, cloudBaseURL, cloudModel, localBaseURL, localModel string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cloudAPIKey = resolveCloudAPIKey(cloudBaseURL, cloudAPIKey)
	p.activeMode = activeMode
	p.cloudAPIKey = cloudAPIKey
	p.cloudBaseURL = cloudBaseURL
	p.cloudModel = cloudModel
	p.localBaseURL = localBaseURL
	p.localModel = localModel

	localEmbed := "nomic-embed-text-v2-moe-GGUF"
	if strings.Contains(localBaseURL, "52625") || strings.Contains(localBaseURL, "18081") || strings.Contains(localBaseURL, "18082") {
		localEmbed = "embed-gemma:300m"
	}
	p.localProvider = NewOpenAICompatibleProvider(localBaseURL, "", localModel, localEmbed)
	p.cloudProvider = NewOpenAICompatibleProvider(cloudBaseURL, cloudAPIKey, cloudModel, "gemini-embedding-2")
	fmt.Printf("[AdaptiveProvider] Config updated: Mode=%s, CloudURL=%s, CloudModel=%s, LocalModel=%s (Embed=%s)\n", activeMode, cloudBaseURL, cloudModel, localModel, localEmbed)
}

func (p *AdaptiveProvider) SetTemperature(temp float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.localProvider != nil {
		p.localProvider.SetTemperature(temp)
	}
	if p.cloudProvider != nil {
		p.cloudProvider.SetTemperature(temp)
	}
}

func (p *AdaptiveProvider) GetActiveMode() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.activeMode
}

func (p *AdaptiveProvider) GetCloudProvider() Provider {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cloudProvider
}

func (p *AdaptiveProvider) handleCloudError(err error) {
	if err == nil {
		return
	}
	errStr := err.Error()
	if strings.Contains(errStr, "429") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") || strings.Contains(errStr, "Quota exceeded") {
		cooldownDuration := 60 * time.Second
		if idx := strings.Index(errStr, "retry in "); idx != -1 {
			var secs int
			if _, scanErr := fmt.Sscanf(errStr[idx:], "retry in %ds", &secs); scanErr == nil && secs > 0 {
				cooldownDuration = time.Duration(secs+5) * time.Second
			}
		}
		p.mu.Lock()
		p.cloudCooldownUntil = time.Now().Add(cooldownDuration)
		p.mu.Unlock()
		fmt.Printf("[AdaptiveProvider] Limite d'API Cloud atteinte (Erreur 429 - Quota dépassé). Pause Cloud de %v. Basculement automatique vers le modèle local (%s)...\n", cooldownDuration.Round(time.Second), p.localModel)
	} else {
		fmt.Printf("[AdaptiveProvider] Appel Cloud échoué : %v. Basculement vers le modèle local...\n", errStr)
	}
}

func (p *AdaptiveProvider) Generate(ctx context.Context, messages []Message) (string, error) {
	return p.GenerateWithMaxTokens(ctx, messages, 4096)
}

func (p *AdaptiveProvider) GenerateWithMaxTokens(ctx context.Context, messages []Message, maxTokens int) (string, error) {
	res, err := p.GenerateDetailed(ctx, messages, maxTokens)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// GenerateDetailed implements DetailedProvider interface for AdaptiveProvider.
func (p *AdaptiveProvider) GenerateDetailed(ctx context.Context, messages []Message, maxTokens int) (GenerationResult, error) {
	p.mu.RLock()
	mode := p.activeMode
	local := p.localProvider
	cloud := p.cloudProvider
	inCooldown := time.Now().Before(p.cloudCooldownUntil)
	p.mu.RUnlock()

	if !inCooldown && (mode == "cloud" || mode == "auto" || mode == "adaptive") {
		if cloud.APIKey != "" {
			cloudCtx := ctx
			if mode == "auto" || mode == "adaptive" {
				timeout := 90 * time.Second
				if maxTokens > 2048 {
					timeout = 180 * time.Second
				}
				var cancel context.CancelFunc
				cloudCtx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			resp, err := cloud.GenerateDetailed(cloudCtx, messages, maxTokens)
			if err == nil {
				return resp, nil
			}
			p.handleCloudError(err)
		} else if mode == "cloud" {
			return GenerationResult{}, fmt.Errorf("cloud mode active but API Key is empty")
		}
	}

	return local.GenerateDetailed(ctx, messages, maxTokens)
}

func (p *AdaptiveProvider) GenerateStream(ctx context.Context, messages []Message) (<-chan string, <-chan error) {
	p.mu.RLock()
	mode := p.activeMode
	local := p.localProvider
	cloud := p.cloudProvider
	inCooldown := time.Now().Before(p.cloudCooldownUntil)
	p.mu.RUnlock()

	if !inCooldown && (mode == "cloud" || mode == "auto" || mode == "adaptive") && cloud.APIKey != "" {
		outChan := make(chan string, 100)
		errChan := make(chan error, 1)

		go func() {
			defer close(outChan)
			defer close(errChan)

			// Try to connect to cloud stream with a timeout for the first token
			cloudCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			cloudOut, cloudErr := cloud.GenerateStream(cloudCtx, messages)

			select {
			case token, ok := <-cloudOut:
				if ok {
					outChan <- token
					// Forward the rest of the stream
					for t := range cloudOut {
						outChan <- t
					}
					return
				}
			case err := <-cloudErr:
				if err != nil {
					p.handleCloudError(err)
				}
			case <-cloudCtx.Done():
				fmt.Println("[AdaptiveProvider] Connexion au stream Cloud expiré. Basculement vers le modèle local...")
			}

			// Fallback to local stream
			localOut, localErr := local.GenerateStream(ctx, messages)
			for {
				select {
				case token, ok := <-localOut:
					if ok {
						outChan <- token
					} else {
						return
					}
				case err := <-localErr:
					if err != nil {
						errChan <- err
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()

		return outChan, errChan
	}

	return local.GenerateStream(ctx, messages)
}

func (p *AdaptiveProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	p.mu.RLock()
	mode := p.activeMode
	local := p.localProvider
	cloud := p.cloudProvider
	inCooldown := time.Now().Before(p.cloudCooldownUntil)
	p.mu.RUnlock()

	if !inCooldown && (mode == "cloud" || mode == "auto" || mode == "adaptive") && cloud.APIKey != "" {
		emb, err := cloud.CreateEmbedding(ctx, text)
		if err == nil {
			return emb, nil
		}
		p.handleCloudError(err)
	}

	return local.CreateEmbedding(ctx, text)
}
