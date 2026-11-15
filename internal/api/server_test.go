package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/voice"
)

func TestAuthMiddleware(t *testing.T) {
	srv := &Server{}
	handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	os.Setenv("PIXEL_API_TOKEN", "secret_freebox_token_123")
	defer os.Unsetenv("PIXEL_API_TOKEN")

	// 1. Localhost request should succeed without token
	reqLocal := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	reqLocal.RemoteAddr = "127.0.0.1:54321"
	recLocal := httptest.NewRecorder()
	handler.ServeHTTP(recLocal, reqLocal)
	if recLocal.Code != http.StatusOK {
		t.Fatalf("expected 200 for localhost without token, got %d", recLocal.Code)
	}

	// 2. External IP without token should fail with 401
	reqExtNoToken := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	reqExtNoToken.RemoteAddr = "82.64.12.34:54321"
	recExtNoToken := httptest.NewRecorder()
	handler.ServeHTTP(recExtNoToken, reqExtNoToken)
	if recExtNoToken.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for external IP without token, got %d", recExtNoToken.Code)
	}

	// 3. External IP with wrong token should fail with 401
	reqExtWrong := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	reqExtWrong.RemoteAddr = "82.64.12.34:54321"
	reqExtWrong.Header.Set("Authorization", "Bearer wrong_token")
	recExtWrong := httptest.NewRecorder()
	handler.ServeHTTP(recExtWrong, reqExtWrong)
	if recExtWrong.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for external IP with wrong token, got %d", recExtWrong.Code)
	}

	// 4. External IP with valid Bearer token should succeed with 200
	reqExtBearer := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	reqExtBearer.RemoteAddr = "82.64.12.34:54321"
	reqExtBearer.Header.Set("Authorization", "Bearer secret_freebox_token_123")
	recExtBearer := httptest.NewRecorder()
	handler.ServeHTTP(recExtBearer, reqExtBearer)
	if recExtBearer.Code != http.StatusOK {
		t.Fatalf("expected 200 for external IP with valid Bearer token, got %d", recExtBearer.Code)
	}

	// 5. External IP with valid query token should succeed with 200
	reqExtQuery := httptest.NewRequest(http.MethodGet, "/api/events?token=secret_freebox_token_123", nil)
	reqExtQuery.RemoteAddr = "82.64.12.34:54321"
	recExtQuery := httptest.NewRecorder()
	handler.ServeHTTP(recExtQuery, reqExtQuery)
	if recExtQuery.Code != http.StatusOK {
		t.Fatalf("expected 200 for external IP with valid query token, got %d", recExtQuery.Code)
	}
}

func TestHandleVisionSettings(t *testing.T) {
	coreMem := memory.NewCoreMemory(t.TempDir() + "/core_memory.json")
	hub := NewEventHub()
	srv := &Server{
		coreMem:  coreMem,
		EventHub: hub,
	}

	// GET default (true)
	reqGet := httptest.NewRequest(http.MethodGet, "/api/vision_settings", nil)
	recGet := httptest.NewRecorder()
	srv.handleVisionSettings(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET, got %d", recGet.Code)
	}
	var got memory.VisionSettings
	if err := json.NewDecoder(recGet.Body).Decode(&got); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if !got.CameraEnabled {
		t.Fatalf("expected camera_enabled=true by default")
	}

	// POST false
	reqPost := httptest.NewRequest(http.MethodPost, "/api/vision_settings", bytes.NewBufferString(`{"camera_enabled":false}`))
	reqPost.Header.Set("Content-Type", "application/json")
	recPost := httptest.NewRecorder()
	srv.handleVisionSettings(recPost, reqPost)
	if recPost.Code != http.StatusOK {
		t.Fatalf("expected 200 on POST false, got %d", recPost.Code)
	}
	if coreMem.IsCameraEnabled() {
		t.Fatalf("expected CoreMemory.IsCameraEnabled() == false after POST")
	}

	// GET again returns false
	recGet2 := httptest.NewRecorder()
	srv.handleVisionSettings(recGet2, httptest.NewRequest(http.MethodGet, "/api/vision_settings", nil))
	var got2 memory.VisionSettings
	if err := json.NewDecoder(recGet2.Body).Decode(&got2); err != nil {
		t.Fatalf("decode GET2 response: %v", err)
	}
	if got2.CameraEnabled {
		t.Fatalf("expected camera_enabled=false after POST")
	}
}

type fakeBridgeSynth struct {
	wav []byte
}

func (f *fakeBridgeSynth) Synthesize(ctx context.Context, text string) ([]byte, error) {
	return append([]byte(nil), f.wav...), nil
}

func TestVoiceBridgeSettingsEndpoint_Rollback(t *testing.T) {
	coreMem := memory.NewCoreMemory(t.TempDir() + "/core.json")
	vb := voice.NewVoiceBridgeManager(voice.DefaultBridgeConfig(), nil)
	srv := &Server{
		coreMem:     coreMem,
		voiceBridge: vb,
	}

	// GET returns default enabled=true
	recGet := httptest.NewRecorder()
	srv.handleVoiceBridgeSettings(recGet, httptest.NewRequest(http.MethodGet, "/api/voice_bridge_settings", nil))
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", recGet.Code)
	}
	var got memory.VoiceBridgeSettings
	if err := json.NewDecoder(recGet.Body).Decode(&got); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if !got.Enabled || got.ThresholdMs <= 0 {
		t.Fatalf("unexpected default settings: %+v", got)
	}

	// POST disabled=false triggers instant rollback in CoreMemory and live VoiceBridgeManager
	got.Enabled = false
	got.ThresholdMs = 750
	payload, _ := json.Marshal(got)
	reqPost := httptest.NewRequest(http.MethodPost, "/api/voice_bridge_settings", bytes.NewReader(payload))
	reqPost.Header.Set("Content-Type", "application/json")
	recPost := httptest.NewRecorder()
	srv.handleVoiceBridgeSettings(recPost, reqPost)
	if recPost.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200: %s", recPost.Code, recPost.Body.String())
	}
	if vb.IsEnabled() {
		t.Fatalf("expected live VoiceBridgeManager.IsEnabled() == false after rollback POST")
	}
	if coreMem.GetVoiceBridgeSettings().Enabled {
		t.Fatalf("expected CoreMemory VoiceBridgeSettings.Enabled == false after rollback POST")
	}
}

func TestVoiceSpeak_ServesPreCachedBridgeAudio(t *testing.T) {
	synth := &fakeBridgeSynth{wav: []byte("RIFFfakebridgewav")}
	cfg := voice.DefaultBridgeConfig()
	cfg.PreCacheAudio = true
	vb := voice.NewVoiceBridgeManager(cfg, synth)
	vb.PreCachePool(context.Background())

	srv := &Server{
		voiceMgr: &voice.VoiceManager{},
	}
	srv.SetVoiceBridge(vb)

	phrase, _ := vb.SelectPhrase(voice.IntentWebSearch)
	req := httptest.NewRequest(http.MethodGet, "/api/voice/speak?text="+url.QueryEscape(phrase), nil)
	rec := httptest.NewRecorder()

	srv.handleVoiceSpeak(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from pre-cached bridge audio, got %d (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("Content-Type = %q, want audio/wav", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), synth.wav) {
		t.Fatalf("unexpected wav bytes: %q", rec.Body.Bytes())
	}
}

func TestServer_IsolatesSTMByClientDevice(t *testing.T) {
	srv := &Server{
		stm: memory.NewSTM(10),
	}
	pcSTM := srv.stmForDevice("pc")
	androidSTM := srv.stmForDevice("android")
	if pcSTM == androidSTM {
		t.Fatalf("expected separate STM instances for pc and android clients")
	}
	pcSTM.AddMessage(llm.Message{Role: llm.RoleUser, Content: "lance Billboard 1986"})
	if len(androidSTM.GetMessages()) != 0 {
		t.Fatalf("expected android STM to remain isolated from PC messages, got %v", androidSTM.GetMessages())
	}
}
