package llm

import (
	"context"
	"fmt"
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

func NewAdaptiveProvider(activeMode, cloudAPIKey, cloudBaseURL, cloudModel, localBaseURL, localModel string) *AdaptiveProvider {
	p := &AdaptiveProvider{
		activeMode:   activeMode,
		cloudAPIKey:  cloudAPIKey,
		cloudBaseURL: cloudBaseURL,
		cloudModel:   cloudModel,
		localBaseURL: localBaseURL,
		localModel:   localModel,
	}
	localEmbed := "nomic-embed-text-v2-moe-GGUF"
	if strings.Contains(localBaseURL, "52625") {
		localEmbed = "embed-gemma:300m"
	}
	p.localProvider = NewOpenAICompatibleProvider(localBaseURL, "", localModel, localEmbed)
	p.cloudProvider = NewOpenAICompatibleProvider(cloudBaseURL, cloudAPIKey, cloudModel, "gemini-embedding-2")
	return p
}

func (p *AdaptiveProvider) UpdateSettings(activeMode, cloudAPIKey, cloudBaseURL, cloudModel, localBaseURL, localModel string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeMode = activeMode
	p.cloudAPIKey = cloudAPIKey
	p.cloudBaseURL = cloudBaseURL
	p.cloudModel = cloudModel
	p.localBaseURL = localBaseURL
	p.localModel = localModel

	localEmbed := "nomic-embed-text-v2-moe-GGUF"
	if strings.Contains(localBaseURL, "52625") {
		localEmbed = "embed-gemma:300m"
	}
	p.localProvider = NewOpenAICompatibleProvider(localBaseURL, "", localModel, localEmbed)
	p.cloudProvider = NewOpenAICompatibleProvider(cloudBaseURL, cloudAPIKey, cloudModel, "gemini-embedding-2")
	fmt.Printf("[AdaptiveProvider] Config updated: Mode=%s, CloudModel=%s, LocalModel=%s (Embed=%s)\n", activeMode, cloudModel, localModel, localEmbed)
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
	return p.GenerateWithMaxTokens(ctx, messages, 2048)
}

func (p *AdaptiveProvider) GenerateWithMaxTokens(ctx context.Context, messages []Message, maxTokens int) (string, error) {
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
				var cancel context.CancelFunc
				cloudCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
			}
			resp, err := cloud.GenerateWithMaxTokens(cloudCtx, messages, maxTokens)
			if err == nil {
				return resp, nil
			}
			p.handleCloudError(err)
		} else if mode == "cloud" {
			return "", fmt.Errorf("cloud mode active but API Key is empty")
		}
	}

	return local.GenerateWithMaxTokens(ctx, messages, maxTokens)
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
