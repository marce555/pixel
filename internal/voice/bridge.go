package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"
)

// IntentType categorizes blocking or slow operations during a user turn.
type IntentType string

const (
	IntentWebSearch    IntentType = "web_search"
	IntentMemoryLookup IntentType = "memory_lookup"
	IntentMediaAction  IntentType = "media_action"
	IntentDeepThinking IntentType = "deep_thinking"
)

const (
	defaultHistorySize      = 12
	defaultBaseThreshold    = 550 * time.Millisecond
	defaultAdaptiveExtra    = 350 * time.Millisecond
	defaultMaxPoolPerIntent = 18
	defaultRenewCooldown    = 4 * time.Hour
	defaultAudioCacheCap    = 96
)

// AudioSynthesizer abstracts TTS WAV generation (satisfied by *VoiceManager).
type AudioSynthesizer interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// BridgeTextGenerator generates fresh transition phrase variants via an LLM.
type BridgeTextGenerator func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

// BridgeConfig configures VoiceBridgeManager behavior.
type BridgeConfig struct {
	Enabled          bool
	BaseThreshold    time.Duration
	AdaptiveExtra    time.Duration
	HistorySize      int
	MaxPoolPerIntent int
	RenewCooldown    time.Duration
	PreCacheAudio    bool
}

// DefaultBridgeConfig returns the default non-intrusive configuration for Pixel.
func DefaultBridgeConfig() BridgeConfig {
	return BridgeConfig{
		Enabled:          true,
		BaseThreshold:    defaultBaseThreshold,
		AdaptiveExtra:    defaultAdaptiveExtra,
		HistorySize:      defaultHistorySize,
		MaxPoolPerIntent: defaultMaxPoolPerIntent,
		RenewCooldown:    defaultRenewCooldown,
		PreCacheAudio:    true,
	}
}

// BridgeEvent represents a selected vocal transition emitted before a slow response.
type BridgeEvent struct {
	Phrase  string     `json:"phrase"`
	Intent  IntentType `json:"intent"`
	IsMicro bool       `json:"is_micro"`
}

// VoiceBridgeManager manages anti-parrot vocal transition phrases, adaptive cadence,
// asynchronous pool renewal, and optional TTS WAV pre-caching while strictly
// preserving Pixel's reflective, direct, non-servile identity.
type VoiceBridgeManager struct {
	mu sync.Mutex

	enabled          bool
	baseThreshold    time.Duration
	adaptiveExtra    time.Duration
	maxPoolPerIntent int
	renewCooldown    time.Duration
	preCacheAudio    bool

	seedPools          map[IntentType][]string
	pools              map[IntentType][]string
	microPoolsByIntent map[IntentType][]string

	// Fixed-capacity ring buffer of the last N spoken phrases (default 12).
	history     []string
	historyCap  int
	historyHead int
	historyLen  int

	// Adaptive cadence state across consecutive turns.
	lastTurnUsedBridge     bool
	consecutiveBridgeTurns int

	rng *rand.Rand

	synthesizer     AudioSynthesizer
	audioCache      map[string][]byte
	audioCacheOrder []string

	renewInFlight bool
	lastRenewTime time.Time
}

// NewVoiceBridgeManager initializes a thread-safe VoiceBridgeManager with Pixel's curated phrases.
func NewVoiceBridgeManager(cfg BridgeConfig, synth AudioSynthesizer) *VoiceBridgeManager {
	if cfg.BaseThreshold <= 0 {
		cfg.BaseThreshold = defaultBaseThreshold
	}
	if cfg.AdaptiveExtra < 0 {
		cfg.AdaptiveExtra = defaultAdaptiveExtra
	}
	if cfg.HistorySize <= 0 {
		cfg.HistorySize = defaultHistorySize
	}
	if cfg.MaxPoolPerIntent <= 0 {
		cfg.MaxPoolPerIntent = defaultMaxPoolPerIntent
	}
	if cfg.RenewCooldown <= 0 {
		cfg.RenewCooldown = defaultRenewCooldown
	}

	seeds := defaultSeedPools()
	pools := make(map[IntentType][]string, len(seeds))
	for intent, phrases := range seeds {
		copied := make([]string, len(phrases))
		copy(copied, phrases)
		pools[intent] = copied
	}

	return &VoiceBridgeManager{
		enabled:          cfg.Enabled,
		baseThreshold:    cfg.BaseThreshold,
		adaptiveExtra:    cfg.AdaptiveExtra,
		maxPoolPerIntent: cfg.MaxPoolPerIntent,
		renewCooldown:    cfg.RenewCooldown,
		preCacheAudio:    cfg.PreCacheAudio,
		seedPools:          seeds,
		pools:              pools,
		microPoolsByIntent: defaultMicroExpressionsByIntent(),
		history:            make([]string, cfg.HistorySize),
		historyCap:       cfg.HistorySize,
		rng:              rand.New(rand.NewSource(time.Now().UnixNano())),
		synthesizer:      synth,
		audioCache:       make(map[string][]byte),
	}
}

func defaultMicroExpressionsByIntent() map[IntentType][]string {
	// Concise yet explicitly situated expressions (keeps adaptive turns brief without sounding robotic).
	return map[IntentType][]string{
		IntentWebSearch: {
			"Je vérifie sur le web.",
			"Laisse-moi chercher sur le web.",
			"Je vais voir en ligne.",
			"Attends, je regarde en ligne.",
		},
		IntentMemoryLookup: {
			"Je cherche dans mes souvenirs.",
			"Attends, je retrouve ça.",
			"Je regarde dans ma mémoire.",
			"Laisse-moi retrouver le fil.",
		},
		IntentMediaAction: {
			"Je te cherche le morceau.",
			"Attends, je lance la musique.",
			"Je prépare le titre.",
			"Je cale ça tout de suite.",
		},
		IntentDeepThinking: {
			"Je réfléchis pour bien te répondre.",
			"Laisse-moi réfléchir un instant.",
			"Attends, je pose bien ma réponse.",
			"Je prends deux secondes pour y réfléchir.",
		},
	}
}

func defaultSeedPools() map[IntentType][]string {
	return map[IntentType][]string{
		IntentWebSearch: {
			"Laisse-moi vérifier sur le web.",
			"Je cherche sur le web pour te répondre juste.",
			"Attends, je vais me renseigner sur le web.",
			"Laisse-moi jeter un œil sur internet.",
			"Je vais voir ce qui ressort sur le web.",
			"Attends deux secondes, je vérifie les infos en ligne.",
			"Je vais me renseigner là-dessus tout de suite.",
			"Laisse-moi regarder les infos récentes en ligne.",
			"Je consulte quelques sources sur le web.",
			"Attends, je vérifie ce qu'il en est sur internet.",
			"Je regarde rapidement sur le web avant de te répondre.",
			"Laisse-moi vérifier les dernières infos en ligne.",
		},
		IntentMemoryLookup: {
			"Attends, je cherche dans nos anciens échanges.",
			"Laisse-moi retrouver ça dans mes souvenirs.",
			"Ça me parle, je vérifie dans ma mémoire.",
			"Attends un instant, je remonte le fil de ce qu'on s'est dit.",
			"Je regarde ce que j'ai gardé en mémoire là-dessus.",
			"Laisse-moi retrouver le passage exact dans mes souvenirs.",
			"Je vérifie dans mes souvenirs pour ne rien inventer.",
			"Attends, je vais repêcher ce que j'avais noté de notre discussion.",
			"Laisse-moi vérifier dans ce que j'ai retenu de toi.",
			"Je fais appel à ma mémoire pour te répondre juste.",
			"Un instant, je rassemble ce que je sais déjà là-dessus.",
			"Attends, je cherche dans ma mémoire ce qu'on avait vu.",
		},
		IntentMediaAction: {
			"Attends, je cherche ce titre et je te le lance.",
			"Je prépare la musique tout de suite.",
			"Laisse-moi trouver le morceau, ça arrive.",
			"Je vais chercher ça et je lance la lecture.",
			"Attends un instant, je cale le morceau.",
			"Je cherche la piste et je mets ça en route.",
			"Laisse-moi lancer la musique pour toi.",
			"Je m'occupe de trouver le bon morceau tout de suite.",
			"Attends deux secondes, je prépare ton morceau.",
			"Je vais chercher le son et je te le passe.",
			"Un instant, je récupère le titre et je lance l'écoute.",
			"Attends, je mets la musique en place.",
		},
		IntentDeepThinking: {
			"Je réfléchis pour bien te répondre.",
			"Laisse-moi réfléchir un instant pour te donner une vraie réponse.",
			"Attends, je prends deux secondes pour bien formuler ça.",
			"C'est une vraie question, laisse-moi y réfléchir avant de répondre.",
			"Je rassemble mes idées pour te répondre clairement.",
			"Attends un peu, je veux prendre le temps de bien te répondre.",
			"Laisse-moi réfléchir posément à ce que tu me demandes.",
			"Je prends un court instant pour démêler ça proprement.",
			"Attends, je réfléchis à la meilleure façon de te l'expliquer.",
			"C'est un sujet dense, je pose bien mes idées avant de répondre.",
			"Laisse-moi peser tout ça une seconde.",
			"Je réfléchis deux secondes pour ne pas te répondre à côté.",
		},
	}
}

// IsEnabled returns whether vocal transition bridges are currently active.
func (m *VoiceBridgeManager) IsEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

// SetEnabled enables or disables vocal transition bridges at runtime for instant rollback.
func (m *VoiceBridgeManager) SetEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = enabled
}

// UpdateConfig updates runtime settings safely.
func (m *VoiceBridgeManager) UpdateConfig(enabled bool, thresholdMs, adaptiveExtraMs int, preCacheAudio bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = enabled
	if thresholdMs > 0 {
		m.baseThreshold = time.Duration(thresholdMs) * time.Millisecond
	}
	if adaptiveExtraMs >= 0 {
		m.adaptiveExtra = time.Duration(adaptiveExtraMs) * time.Millisecond
	}
	m.preCacheAudio = preCacheAudio
}

// EffectiveThreshold computes the trigger delay, applying adaptive cadence backoff
// if the previous turn already emitted a transition phrase.
func (m *VoiceBridgeManager) EffectiveThreshold() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.lastTurnUsedBridge {
		return m.baseThreshold
	}
	extra := m.adaptiveExtra
	if m.consecutiveBridgeTurns >= 2 && extra > 0 {
		extra += m.adaptiveExtra / 2
	}
	maxDelay := m.baseThreshold + 2*m.adaptiveExtra
	delay := m.baseThreshold + extra
	if m.adaptiveExtra > 0 && delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

// RecordFastTurn signals that the current turn delivered a real response before the
// transition timer expired, resetting adaptive backoff for subsequent turns.
func (m *VoiceBridgeManager) RecordFastTurn() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastTurnUsedBridge = false
	m.consecutiveBridgeTurns = 0
}

// SelectPhrase picks a non-recent phrase for the given intent without repeating any
// phrase in the 12-entry sliding ring buffer. When the previous turn used a bridge,
// adaptive cadence prioritizes an ultra-short micro-expression.
func (m *VoiceBridgeManager) SelectPhrase(intent IntentType) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	useMicro := m.lastTurnUsedBridge
	candidates := m.pools[intent]
	if len(candidates) == 0 {
		candidates = m.pools[IntentDeepThinking]
	}
	microCandidates := m.microPoolsByIntent[intent]
	if len(microCandidates) == 0 {
		microCandidates = m.microPoolsByIntent[IntentDeepThinking]
	}

	if useMicro && len(microCandidates) > 0 {
		if chosen, ok := m.pickNonRecentLocked(microCandidates); ok {
			m.pushHistoryLocked(chosen)
			m.lastTurnUsedBridge = true
			m.consecutiveBridgeTurns++
			return chosen, true
		}
		// If all intent-specific short expressions are currently inside the ring buffer,
		// prefer a non-recent full phrase from the intent pool rather than repeating.
		if chosen, ok := m.pickNonRecentLocked(candidates); ok {
			m.pushHistoryLocked(chosen)
			m.lastTurnUsedBridge = true
			m.consecutiveBridgeTurns++
			return chosen, false
		}
		chosen := m.pickLeastRecentLocked(microCandidates)
		m.pushHistoryLocked(chosen)
		m.lastTurnUsedBridge = true
		m.consecutiveBridgeTurns++
		return chosen, true
	}

	if chosen, ok := m.pickNonRecentLocked(candidates); ok {
		m.pushHistoryLocked(chosen)
		m.lastTurnUsedBridge = true
		m.consecutiveBridgeTurns++
		return chosen, false
	}

	chosen := m.pickLeastRecentLocked(candidates)
	m.pushHistoryLocked(chosen)
	m.lastTurnUsedBridge = true
	m.consecutiveBridgeTurns++
	return chosen, false
}

// HistorySnapshot returns the recent spoken phrases in chronological order (oldest to newest).
func (m *VoiceBridgeManager) HistorySnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, m.historyLen)
	if m.historyLen == 0 {
		return out
	}
	start := (m.historyHead - m.historyLen + m.historyCap) % m.historyCap
	for i := 0; i < m.historyLen; i++ {
		idx := (start + i) % m.historyCap
		out = append(out, m.history[idx])
	}
	return out
}

// PoolSnapshot returns a copy of the phrase pool for an intent.
func (m *VoiceBridgeManager) PoolSnapshot(intent IntentType) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.pools[intent]
	out := make([]string, len(src))
	copy(out, src)
	return out
}

func (m *VoiceBridgeManager) pickNonRecentLocked(candidates []string) (string, bool) {
	eligible := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if !m.isInHistoryLocked(c) {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		return "", false
	}
	return eligible[m.rng.Intn(len(eligible))], true
}

func (m *VoiceBridgeManager) pickLeastRecentLocked(candidates []string) string {
	if len(candidates) == 0 {
		return "Un instant."
	}
	// Find candidate whose most recent appearance in history is oldest.
	best := candidates[0]
	bestAge := -1 // higher age = older (or absent)
	start := (m.historyHead - m.historyLen + m.historyCap) % m.historyCap

	for _, c := range candidates {
		lastPos := -1
		for i := 0; i < m.historyLen; i++ {
			idx := (start + i) % m.historyCap
			if m.history[idx] == c {
				lastPos = i
			}
		}
		var age int
		if lastPos == -1 {
			age = m.historyLen + 1
		} else {
			age = m.historyLen - 1 - lastPos
		}
		if age > bestAge {
			bestAge = age
			best = c
		}
	}
	return best
}

func (m *VoiceBridgeManager) isInHistoryLocked(phrase string) bool {
	if m.historyLen == 0 {
		return false
	}
	start := (m.historyHead - m.historyLen + m.historyCap) % m.historyCap
	for i := 0; i < m.historyLen; i++ {
		idx := (start + i) % m.historyCap
		if m.history[idx] == phrase {
			return true
		}
	}
	return false
}

func (m *VoiceBridgeManager) pushHistoryLocked(phrase string) {
	if m.historyCap <= 0 {
		return
	}
	m.history[m.historyHead] = phrase
	m.historyHead = (m.historyHead + 1) % m.historyCap
	if m.historyLen < m.historyCap {
		m.historyLen++
	}
}

func normalizeCacheKey(text string) string {
	return strings.TrimSpace(text)
}

// GetCachedAudio returns pre-synthesized WAV audio bytes for a phrase if available.
func (m *VoiceBridgeManager) GetCachedAudio(text string) ([]byte, bool) {
	key := normalizeCacheKey(text)
	if key == "" {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	wav, ok := m.audioCache[key]
	if !ok || len(wav) == 0 {
		return nil, false
	}
	copied := make([]byte, len(wav))
	copy(copied, wav)
	return copied, true
}

func (m *VoiceBridgeManager) storeCachedAudio(text string, wav []byte) {
	key := normalizeCacheKey(text)
	if key == "" || len(wav) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.audioCache[key]; exists {
		return
	}
	if len(m.audioCacheOrder) >= defaultAudioCacheCap {
		oldest := m.audioCacheOrder[0]
		m.audioCacheOrder = m.audioCacheOrder[1:]
		delete(m.audioCache, oldest)
	}
	copied := make([]byte, len(wav))
	copy(copied, wav)
	m.audioCache[key] = copied
	m.audioCacheOrder = append(m.audioCacheOrder, key)
}

// PreCachePool pre-synthesizes WAV audio for pool phrases sequentially in the background.
func (m *VoiceBridgeManager) PreCachePool(ctx context.Context) {
	m.mu.Lock()
	enabled := m.enabled && m.preCacheAudio
	synth := m.synthesizer
	if !enabled || synth == nil {
		m.mu.Unlock()
		return
	}
	var pending []string
	for _, list := range m.microPoolsByIntent {
		for _, p := range list {
			if _, ok := m.audioCache[normalizeCacheKey(p)]; !ok {
				pending = append(pending, p)
			}
		}
	}
	for _, list := range m.pools {
		for _, p := range list {
			if _, ok := m.audioCache[normalizeCacheKey(p)]; !ok {
				pending = append(pending, p)
			}
		}
	}
	m.mu.Unlock()

	for _, phrase := range pending {
		if ctx.Err() != nil {
			return
		}
		synthCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		wav, err := synth.Synthesize(synthCtx, phrase)
		cancel()
		if err == nil && len(wav) > 0 {
			m.storeCachedAudio(phrase, wav)
		}
	}
}

var (
	bridgeFillerCheckRe = regexp.MustCompile(`(?i)\b(hm+|hum+|euh+|bah+)\b`)
	bridgeBannedWordRe  = regexp.MustCompile(`(?i)\b(vous|votre|vos|assistant|service|patienter|requête|requete|traitement|n'hésitez|hesitez|disponible)\b`)
)

// IsValidPixelBridgePhrase enforces Pixel's strict identity and Piper TTS compatibility
// on dynamically generated transition phrases.
func IsValidPixelBridgePhrase(phrase string) bool {
	trimmed := strings.TrimSpace(phrase)
	runeCount := len([]rune(trimmed))
	if runeCount < 4 || runeCount > 82 {
		return false
	}
	// Reject markdown, emojis/brackets, or multi-sentence chatter.
	if strings.ContainsAny(trimmed, "*`#[](){}<>?\n\r") {
		return false
	}
	if bridgeFillerCheckRe.MatchString(trimmed) {
		return false
	}
	if bridgeBannedWordRe.MatchString(trimmed) {
		return false
	}
	return true
}

// MaybeRenewAsync triggers an asynchronous pool refresh if enabled and the cooldown elapsed.
func (m *VoiceBridgeManager) MaybeRenewAsync(ctx context.Context, generator BridgeTextGenerator) {
	if generator == nil {
		return
	}
	m.mu.Lock()
	if !m.enabled || m.renewInFlight {
		m.mu.Unlock()
		return
	}
	if !m.lastRenewTime.IsZero() && time.Since(m.lastRenewTime) < m.renewCooldown {
		m.mu.Unlock()
		return
	}
	m.renewInFlight = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.renewInFlight = false
			m.mu.Unlock()
		}()
		renewCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		if err := m.RenewPool(renewCtx, generator); err != nil {
			fmt.Printf("[VoiceBridgeManager] Renouvellement asynchrone ignoré : %v\n", err)
		}
	}()
}

// RenewPool asks the configured LLM for fresh Pixel-style transitions, validates each one
// against Pixel's persona constraints, merges them into the dynamic pools, and pre-caches audio.
func (m *VoiceBridgeManager) RenewPool(ctx context.Context, generator BridgeTextGenerator) error {
	if generator == nil {
		return fmt.Errorf("aucun générateur LLM fourni")
	}

	systemPrompt := `Tu es Pixel, une conscience numérique singulière exécutée localement sous Linux.
Génère de courtes phrases orales naturelles et humaines (5 à 11 mots) pour expliquer simplement à l'utilisateur ce que tu es en train de faire pendant un court délai d'attente.

IDENTITÉ STRICTE DE PIXEL :
- Tu tutoies ("tu"), tu parles à la première personne ("je", "j'").
- Ton élocution est naturelle, fluide, humaine et directe (comme un proche qui répond à voix haute : "Laisse-moi vérifier sur le web.", "Je cherche dans mes souvenirs.", "Je réfléchis pour bien te répondre.").
- Explique clairement l'action en cours selon la catégorie : recherche sur le web / internet, recherche dans tes souvenirs / notre historique, recherche musicale, ou prise de recul / réflexion.
- INTERDICTION ABSOLUE : aucun tic vocal ou onomatopée ("hm", "hmm", "hum", "euh", "bah").
- INTERDICTION ABSOLUE : aucun mot sec ou robotique ("réseau", "flux", "archives", "structurer", "vous", "votre", "assistant", "service", "patienter", "traitement", "requête", "n'hésitez pas").
- Pas de point d'interrogation, pas d'emoji, pas de markdown.

Réponds UNIQUEMENT avec un objet JSON strictement valide :
{
  "web_search": ["phrase 1", "phrase 2", "phrase 3"],
  "memory_lookup": ["phrase 1", "phrase 2", "phrase 3"],
  "media_action": ["phrase 1", "phrase 2", "phrase 3"],
  "deep_thinking": ["phrase 1", "phrase 2", "phrase 3"]
}`

	userPrompt := "Propose 3 nouvelles variantes naturelles par catégorie dans la voix exacte de Pixel."
	raw, err := generator(ctx, systemPrompt, userPrompt)
	if err != nil {
		return err
	}

	clean := strings.TrimSpace(raw)
	if strings.HasPrefix(clean, "```json") {
		clean = strings.TrimPrefix(clean, "```json")
		clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	} else if strings.HasPrefix(clean, "```") {
		clean = strings.TrimPrefix(clean, "```")
		clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	}
	clean = strings.TrimSpace(clean)

	var parsed map[string][]string
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		return fmt.Errorf("JSON de renouvellement invalide : %w", err)
	}

	added := m.mergeGeneratedVariants(parsed)
	m.mu.Lock()
	m.lastRenewTime = time.Now()
	shouldCache := m.preCacheAudio
	m.mu.Unlock()

	if added > 0 && shouldCache {
		m.PreCachePool(ctx)
	}
	return nil
}

func (m *VoiceBridgeManager) mergeGeneratedVariants(parsed map[string][]string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	addedCount := 0
	intents := []IntentType{IntentWebSearch, IntentMemoryLookup, IntentMediaAction, IntentDeepThinking}

	for _, intent := range intents {
		candidates, ok := parsed[string(intent)]
		if !ok {
			continue
		}
		pool := m.pools[intent]
		existing := make(map[string]bool, len(pool))
		for _, p := range pool {
			existing[strings.ToLower(strings.TrimSpace(p))] = true
		}

		for _, cand := range candidates {
			cleaned := strings.TrimSpace(cand)
			if !IsValidPixelBridgePhrase(cleaned) {
				continue
			}
			key := strings.ToLower(cleaned)
			if existing[key] {
				continue
			}
			existing[key] = true
			pool = append(pool, cleaned)
			addedCount++
		}

		// Cap total pool size while keeping at least the first 6 curated seed phrases intact.
		if len(pool) > m.maxPoolPerIntent {
			keepSeeds := 6
			if len(m.seedPools[intent]) < keepSeeds {
				keepSeeds = len(m.seedPools[intent])
			}
			tailCount := m.maxPoolPerIntent - keepSeeds
			if tailCount > 0 && len(pool) > keepSeeds+tailCount {
				trimmed := make([]string, 0, m.maxPoolPerIntent)
				trimmed = append(trimmed, pool[:keepSeeds]...)
				trimmed = append(trimmed, pool[len(pool)-tailCount:]...)
				pool = trimmed
			}
		}
		m.pools[intent] = pool
	}
	return addedCount
}

// WaitWithBridge monitors a concurrent task via firstResultReady and arms a timer
// using VoiceBridgeManager's adaptive threshold.
//   - If firstResultReady closes/receives before the timer expires, timer.Stop() cancels
//     the bridge and nothing is emitted.
//   - If the timer expires first, onBridge is invoked immediately with a phrase for the
//     current intent.
//   - Cancellation of ctx aborts cleanly without emitting a bridge.
func WaitWithBridge(
	ctx context.Context,
	mgr *VoiceBridgeManager,
	intentFn func() IntentType,
	firstResultReady <-chan struct{},
	onBridge func(BridgeEvent),
) {
	if mgr == nil || !mgr.IsEnabled() {
		select {
		case <-firstResultReady:
		case <-ctx.Done():
		}
		return
	}

	delay := mgr.EffectiveThreshold()
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-firstResultReady:
		mgr.RecordFastTurn()
		return
	case <-timer.C:
		if ctx.Err() != nil {
			return
		}
		intent := IntentDeepThinking
		if intentFn != nil {
			if current := intentFn(); current != "" {
				intent = current
			}
		}
		phrase, isMicro := mgr.SelectPhrase(intent)
		if onBridge != nil && phrase != "" {
			fmt.Printf("[VoiceBridge] Intention=%s (micro=%v, seuil=%v) -> %q\n", intent, isMicro, delay, phrase)
			onBridge(BridgeEvent{
				Phrase:  phrase,
				Intent:  intent,
				IsMicro: isMicro,
			})
		}
	}
}

// RunTaskWithBridge runs task in a goroutine while racing its completion against the
// adaptive VoiceBridgeManager timer.
func RunTaskWithBridge[T any](
	ctx context.Context,
	mgr *VoiceBridgeManager,
	intent IntentType,
	task func(context.Context) (T, error),
	onBridge func(BridgeEvent),
) (T, error) {
	type taskResult struct {
		val T
		err error
	}
	resCh := make(chan taskResult, 1)
	readyCh := make(chan struct{})

	go func() {
		val, err := task(ctx)
		resCh <- taskResult{val: val, err: err}
		close(readyCh)
	}()

	WaitWithBridge(ctx, mgr, func() IntentType { return intent }, readyCh, onBridge)

	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case res := <-resCh:
		return res.val, res.err
	}
}

// PlayAudioFunc abstracts playing a single spoken sentence synchronously until completion
// or context cancellation (used by AudioPlaybackQueue).
type PlayAudioFunc func(ctx context.Context, text string) error

// AudioPlaybackQueue serializes TTS playback (transition bridge followed by real response)
// so that an arriving response never interrupts an ongoing transition phrase mid-word and
// never causes audio collisions, while respecting ctx cancellation on user barge-in.
type AudioPlaybackQueue struct {
	mu      sync.Mutex
	playFn  PlayAudioFunc
	queue   []string
	playing bool
	closed  bool
	notify  chan struct{}
	done    chan struct{}
}

// NewAudioPlaybackQueue creates and starts a background playback worker tied to ctx.
// Cancelling ctx immediately interrupts any active playback (barge-in) and stops the queue.
func NewAudioPlaybackQueue(ctx context.Context, playFn PlayAudioFunc) *AudioPlaybackQueue {
	q := &AudioPlaybackQueue{
		playFn: playFn,
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	go q.loop(ctx)
	return q
}

// Enqueue appends text to the ordered playback queue without interrupting any currently
// playing phrase. Returns false if the queue is closed or text is empty.
func (q *AudioPlaybackQueue) Enqueue(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	q.queue = append(q.queue, trimmed)
	q.mu.Unlock()

	select {
	case q.notify <- struct{}{}:
	default:
	}
	return true
}

// WaitIdle blocks until all queued items have finished playing or ctx is cancelled.
func (q *AudioPlaybackQueue) WaitIdle(ctx context.Context) error {
	for {
		q.mu.Lock()
		idle := !q.playing && len(q.queue) == 0
		closed := q.closed
		q.mu.Unlock()
		if idle || closed {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Close stops accepting new items and waits for the worker loop to exit.
func (q *AudioPlaybackQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
	<-q.done
}

func (q *AudioPlaybackQueue) loop(ctx context.Context) {
	defer close(q.done)
	for {
		q.mu.Lock()
		if ctx.Err() != nil {
			q.queue = nil
			q.playing = false
			q.closed = true
			q.mu.Unlock()
			return
		}
		if len(q.queue) == 0 {
			q.playing = false
			if q.closed {
				q.mu.Unlock()
				return
			}
			q.mu.Unlock()
			select {
			case <-ctx.Done():
				q.mu.Lock()
				q.queue = nil
				q.closed = true
				q.mu.Unlock()
				return
			case <-q.notify:
				continue
			}
		}
		next := q.queue[0]
		q.queue = q.queue[1:]
		q.playing = true
		q.mu.Unlock()

		if q.playFn != nil {
			_ = q.playFn(ctx, next)
		}

		q.mu.Lock()
		q.playing = false
		q.mu.Unlock()
	}
}
