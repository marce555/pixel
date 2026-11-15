package voice

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockSynthesizer struct {
	mu    sync.Mutex
	calls []string
}

func (m *mockSynthesizer) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	m.mu.Lock()
	m.calls = append(m.calls, text)
	m.mu.Unlock()
	return []byte("RIFF" + text), nil
}

func (m *mockSynthesizer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func TestVoiceBridgeManager_NoRepetitionRingBuffer(t *testing.T) {
	mgr := NewVoiceBridgeManager(DefaultBridgeConfig(), nil)

	intents := []IntentType{
		IntentWebSearch,
		IntentMemoryLookup,
		IntentMediaAction,
		IntentDeepThinking,
	}

	// 12 consecutive picks for a single intent must all be unique (ring buffer capacity = 12).
	seen := make(map[string]bool)
	for i := 0; i < 12; i++ {
		phrase, _ := mgr.SelectPhrase(IntentWebSearch)
		if phrase == "" {
			t.Fatalf("call %d returned empty phrase", i)
		}
		if seen[phrase] {
			t.Fatalf("call %d repeated phrase %q inside 12-phrase window", i, phrase)
		}
		seen[phrase] = true
	}

	hist := mgr.HistorySnapshot()
	if len(hist) != 12 {
		t.Fatalf("expected ring buffer history length 12, got %d", len(hist))
	}

	// Check mixed intents across another 12-turn sliding window with fast turns in between.
	for i := 0; i < 24; i++ {
		intent := intents[i%len(intents)]
		if i%2 == 0 {
			mgr.RecordFastTurn()
		}
		phrase, _ := mgr.SelectPhrase(intent)
		snap := mgr.HistorySnapshot()
		if len(snap) > 12 {
			t.Fatalf("history exceeded cap 12: got %d", len(snap))
		}
		// Last element must match phrase, and phrase must appear only once in current 12-slot window.
		count := 0
		for _, h := range snap {
			if h == phrase {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("phrase %q appeared %d times in current 12-slot history: %v", phrase, count, snap)
		}
	}
}

func TestVoiceBridgeManager_AdaptiveCadence(t *testing.T) {
	cfg := DefaultBridgeConfig()
	cfg.BaseThreshold = 550 * time.Millisecond
	cfg.AdaptiveExtra = 350 * time.Millisecond
	mgr := NewVoiceBridgeManager(cfg, nil)

	if got := mgr.EffectiveThreshold(); got != 550*time.Millisecond {
		t.Fatalf("initial threshold = %v, want 550ms", got)
	}

	// First bridge turn -> full intent phrase (not micro), raises next threshold.
	phrase1, isMicro1 := mgr.SelectPhrase(IntentMemoryLookup)
	if phrase1 == "" || isMicro1 {
		t.Fatalf("first bridge turn got (%q, isMicro=%v), want non-empty & isMicro=false", phrase1, isMicro1)
	}

	if got := mgr.EffectiveThreshold(); got != 900*time.Millisecond {
		t.Fatalf("after 1 bridge turn threshold = %v, want 900ms", got)
	}

	// Consecutive bridge turn -> should prioritize concise yet intent-situated micro-expression.
	phrase2, isMicro2 := mgr.SelectPhrase(IntentWebSearch)
	if phrase2 == "" || !isMicro2 {
		t.Fatalf("consecutive bridge turn got (%q, isMicro=%v), want non-empty & isMicro=true", phrase2, isMicro2)
	}
	lower2 := strings.ToLower(phrase2)
	if !strings.Contains(lower2, "web") && !strings.Contains(lower2, "ligne") {
		t.Fatalf("expected web-situated micro-expression, got %q", phrase2)
	}
	if got := mgr.EffectiveThreshold(); got <= 900*time.Millisecond {
		t.Fatalf("after consecutive bridge turns threshold = %v, want > 900ms", got)
	}

	// Fast turn resets adaptive cadence back to base threshold and non-micro priority.
	mgr.RecordFastTurn()
	if got := mgr.EffectiveThreshold(); got != 550*time.Millisecond {
		t.Fatalf("after fast turn threshold = %v, want 550ms", got)
	}
	_, isMicro3 := mgr.SelectPhrase(IntentWebSearch)
	if isMicro3 {
		t.Fatalf("after fast turn expected full phrase (isMicro=false), got isMicro=true")
	}
}

func TestIsValidPixelBridgePhrase_StrictPersonaGuard(t *testing.T) {
	rejected := []string{
		"",
		"Hmm...",
		"Euh, laisse-moi chercher.",
		"Hum, voyons voir.",
		"Bah attends.",
		"Je vous cherche ça tout de suite.",
		"En tant qu'assistant IA, je vérifie.",
		"Veuillez patienter un instant.",
		"Merci de patienter.",
		"Comment puis-je t'aider ?",
		"*Je cherche dans mes notes*",
		"Phrase beaucoup trop longue pour une simple transition vocale naturelle dans le flux audio de Pixel.",
	}
	for _, p := range rejected {
		if IsValidPixelBridgePhrase(p) {
			t.Errorf("expected phrase %q to be rejected by persona guard", p)
		}
	}

	accepted := []string{
		"Je vérifie ça.",
		"Laisse-moi fouiller un peu.",
		"Voyons ce que je retrouve.",
		"Attends, je jette un œil.",
	}
	for _, p := range accepted {
		if !IsValidPixelBridgePhrase(p) {
			t.Errorf("expected phrase %q to be accepted by persona guard", p)
		}
	}
}

func TestVoiceBridgeManager_RenewPoolAndPreCache(t *testing.T) {
	synth := &mockSynthesizer{}
	cfg := DefaultBridgeConfig()
	cfg.RenewCooldown = 0
	cfg.PreCacheAudio = true
	mgr := NewVoiceBridgeManager(cfg, synth)

	gen := func(ctx context.Context, sys, usr string) (string, error) {
		return `{
			"web_search": ["Hmm... je cherche.", "Je vais voir ce qui sort là-dessus.", "Veuillez patienter."],
			"memory_lookup": ["Je remonte le fil.", "Je vous cherche ça."],
			"media_action": ["Je cale le morceau."],
			"deep_thinking": ["Je pose ça proprement."]
		}`, nil
	}

	if err := mgr.RenewPool(context.Background(), gen); err != nil {
		t.Fatalf("RenewPool failed: %v", err)
	}

	webPool := mgr.PoolSnapshot(IntentWebSearch)
	hasValid := false
	for _, p := range webPool {
		if p == "Je vais voir ce qui sort là-dessus." {
			hasValid = true
		}
		if strings.Contains(p, "Hmm") || strings.Contains(p, "patienter") {
			t.Fatalf("invalid phrase leaked into web pool: %q", p)
		}
	}
	if !hasValid {
		t.Fatalf("expected valid renewed phrase in web pool, got %v", webPool)
	}

	if synth.callCount() == 0 {
		t.Fatalf("expected PreCacheAudio to invoke synthesizer during RenewPool")
	}

	wav, ok := mgr.GetCachedAudio("Je vais voir ce qui sort là-dessus.")
	if !ok || !bytes.Equal(wav, []byte("RIFFJe vais voir ce qui sort là-dessus.")) {
		t.Fatalf("expected cached WAV for renewed phrase, got ok=%v len=%d", ok, len(wav))
	}
}

func TestVoiceBridgeManager_ConcurrentSafety(t *testing.T) {
	synth := &mockSynthesizer{}
	mgr := NewVoiceBridgeManager(DefaultBridgeConfig(), synth)

	var wg sync.WaitGroup
	intents := []IntentType{IntentWebSearch, IntentMemoryLookup, IntentMediaAction, IntentDeepThinking}

	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				intent := intents[(id+i)%len(intents)]
				_ = mgr.EffectiveThreshold()
				p, _ := mgr.SelectPhrase(intent)
				_ = mgr.HistorySnapshot()
				_, _ = mgr.GetCachedAudio(p)
				if i%5 == 0 {
					mgr.RecordFastTurn()
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestWaitAndRunTaskWithBridge_FastVsSlowAndCancel(t *testing.T) {
	t.Run("fast response cancels timer without bridge", func(t *testing.T) {
		cfg := DefaultBridgeConfig()
		cfg.BaseThreshold = 150 * time.Millisecond
		mgr := NewVoiceBridgeManager(cfg, nil)

		var bridges []BridgeEvent
		res, err := RunTaskWithBridge(
			context.Background(),
			mgr,
			IntentWebSearch,
			func(ctx context.Context) (string, error) {
				time.Sleep(15 * time.Millisecond)
				return "fast-answer", nil
			},
			func(ev BridgeEvent) {
				bridges = append(bridges, ev)
			},
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != "fast-answer" {
			t.Fatalf("res = %q, want fast-answer", res)
		}
		if len(bridges) != 0 {
			t.Fatalf("expected 0 bridges on fast response, got %+v", bridges)
		}
	})

	t.Run("slow response triggers one bridge then delivers task result", func(t *testing.T) {
		cfg := DefaultBridgeConfig()
		cfg.BaseThreshold = 35 * time.Millisecond
		mgr := NewVoiceBridgeManager(cfg, nil)

		var mu sync.Mutex
		var bridges []BridgeEvent
		res, err := RunTaskWithBridge(
			context.Background(),
			mgr,
			IntentMemoryLookup,
			func(ctx context.Context) (string, error) {
				time.Sleep(120 * time.Millisecond)
				return "slow-answer", nil
			},
			func(ev BridgeEvent) {
				mu.Lock()
				bridges = append(bridges, ev)
				mu.Unlock()
			},
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != "slow-answer" {
			t.Fatalf("res = %q, want slow-answer", res)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(bridges) != 1 {
			t.Fatalf("expected 1 bridge on slow response, got %d (%+v)", len(bridges), bridges)
		}
		if bridges[0].Intent != IntentMemoryLookup || bridges[0].Phrase == "" {
			t.Fatalf("unexpected bridge event: %+v", bridges[0])
		}
	})

	t.Run("rollback disable prevents bridge emission", func(t *testing.T) {
		cfg := DefaultBridgeConfig()
		cfg.BaseThreshold = 20 * time.Millisecond
		mgr := NewVoiceBridgeManager(cfg, nil)
		mgr.SetEnabled(false)

		var count int32
		res, err := RunTaskWithBridge(
			context.Background(),
			mgr,
			IntentDeepThinking,
			func(ctx context.Context) (string, error) {
				time.Sleep(60 * time.Millisecond)
				return "done", nil
			},
			func(ev BridgeEvent) {
				atomic.AddInt32(&count, 1)
			},
		)
		if err != nil || res != "done" {
			t.Fatalf("unexpected result (%q, %v)", res, err)
		}
		if atomic.LoadInt32(&count) != 0 {
			t.Fatalf("expected 0 bridges when disabled, got %d", count)
		}
	})

	t.Run("context cancel barge-in aborts cleanly with no bridge", func(t *testing.T) {
		cfg := DefaultBridgeConfig()
		cfg.BaseThreshold = 150 * time.Millisecond
		mgr := NewVoiceBridgeManager(cfg, nil)

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(25 * time.Millisecond)
			cancel()
		}()

		var count int32
		ready := make(chan struct{})
		WaitWithBridge(ctx, mgr, func() IntentType { return IntentMediaAction }, ready, func(ev BridgeEvent) {
			atomic.AddInt32(&count, 1)
		})
		if atomic.LoadInt32(&count) != 0 {
			t.Fatalf("expected 0 bridges after cancel, got %d", count)
		}
	})
}

func TestAudioPlaybackQueue_SequentialChainingAndBargeIn(t *testing.T) {
	t.Run("chains real response cleanly after transition finishes", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var mu sync.Mutex
		var events []string
		bridgeStarted := make(chan struct{})
		releaseBridge := make(chan struct{})

		q := NewAudioPlaybackQueue(ctx, func(pCtx context.Context, text string) error {
			mu.Lock()
			events = append(events, "start:"+text)
			mu.Unlock()

			if text == "bridge-phrase" {
				close(bridgeStarted)
				select {
				case <-releaseBridge:
				case <-pCtx.Done():
					return pCtx.Err()
				}
			} else {
				time.Sleep(10 * time.Millisecond)
			}

			mu.Lock()
			events = append(events, "end:"+text)
			mu.Unlock()
			return nil
		})
		defer q.Close()

		if !q.Enqueue("bridge-phrase") {
			t.Fatal("failed to enqueue bridge")
		}
		<-bridgeStarted

		// Real response arrives while bridge is still playing.
		if !q.Enqueue("real-answer") {
			t.Fatal("failed to enqueue real-answer")
		}

		// Yield briefly to prove real-answer has NOT started prematurely.
		time.Sleep(25 * time.Millisecond)
		mu.Lock()
		earlySnapshot := append([]string(nil), events...)
		mu.Unlock()
		for _, e := range earlySnapshot {
			if e == "start:real-answer" {
				t.Fatalf("real-answer started before bridge finished: %v", earlySnapshot)
			}
		}

		close(releaseBridge)
		if err := q.WaitIdle(ctx); err != nil {
			t.Fatalf("WaitIdle failed: %v", err)
		}

		want := []string{
			"start:bridge-phrase",
			"end:bridge-phrase",
			"start:real-answer",
			"end:real-answer",
		}
		mu.Lock()
		defer mu.Unlock()
		if fmt.Sprint(events) != fmt.Sprint(want) {
			t.Fatalf("events = %v, want %v", events, want)
		}
	})

	t.Run("barge-in context cancellation interrupts playback and skips queued items", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		var firstAborted int32
		var playedSecond int32

		q := NewAudioPlaybackQueue(ctx, func(pCtx context.Context, text string) error {
			if text == "first" {
				close(started)
				<-pCtx.Done()
				atomic.StoreInt32(&firstAborted, 1)
				return pCtx.Err()
			}
			atomic.AddInt32(&playedSecond, 1)
			return nil
		})
		defer q.Close()

		q.Enqueue("first")
		<-started
		q.Enqueue("second-never-played")

		cancel()
		if err := q.WaitIdle(context.Background()); err != nil {
			t.Fatalf("WaitIdle failed after barge-in close: %v", err)
		}
		if atomic.LoadInt32(&firstAborted) != 1 {
			t.Fatalf("expected active playback to observe barge-in cancellation")
		}
		if atomic.LoadInt32(&playedSecond) != 0 {
			t.Fatalf("queued second phrase must not play after barge-in")
		}
	})
}
