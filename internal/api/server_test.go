package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/scheduler"
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

type fakeSpeakerExtractor struct{}

func (f *fakeSpeakerExtractor) IsReady(_ context.Context) bool { return true }
func (f *fakeSpeakerExtractor) ExtractEmbedding(_ context.Context, _ string) (voice.EmbeddingResult, error) {
	return voice.EmbeddingResult{Dim: 4, DurationMs: 1800, InferMs: 12, Embedding: []float64{1, 0, 0, 0}}, nil
}

func TestVoiceSpeakerProfileEndpoints(t *testing.T) {
	sv := voice.NewSpeakerVerifierWithExtractor(t.TempDir()+"/speaker.json", &fakeSpeakerExtractor{})
	srv := &Server{}
	srv.SetSpeakerVerifier(sv)

	// Enroll directly via verifier, then inspect and toggle settings via HTTP handler.
	res, err := sv.EnrollWav(context.Background(), "sample.wav", "pc", "Bonjour Pixel")
	if err != nil {
		t.Fatalf("enroll failed: %v", err)
	}

	recGet := httptest.NewRecorder()
	srv.handleVoiceSpeakerProfile(recGet, httptest.NewRequest(http.MethodGet, "/api/voice/speaker/profile", nil))
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", recGet.Code)
	}
	var status voice.SpeakerStatus
	if err := json.NewDecoder(recGet.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.Enrolled || status.SampleCount != 1 || !status.ObserveOnly {
		t.Fatalf("unexpected speaker status: %+v", status)
	}

	// Update threshold + hot-enable handsfree_no_wake
	recPost := httptest.NewRecorder()
	reqPost := httptest.NewRequest(http.MethodPost, "/api/voice/speaker/profile", bytes.NewBufferString(`{"threshold":0.68,"handsfree_no_wake":true}`))
	reqPost.Header.Set("Content-Type", "application/json")
	srv.handleVoiceSpeakerProfile(recPost, reqPost)
	if recPost.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200: %s", recPost.Code, recPost.Body.String())
	}
	var updated voice.SpeakerStatus
	if err := json.NewDecoder(recPost.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated status: %v", err)
	}
	if updated.ObserveOnly || !updated.HandsFreeNoWake || updated.Threshold != 0.68 {
		t.Fatalf("unexpected updated settings: %+v", updated)
	}

	// Delete sample by ID
	recDel := httptest.NewRecorder()
	srv.handleVoiceSpeakerProfile(recDel, httptest.NewRequest(http.MethodDelete, "/api/voice/speaker/profile?id="+url.QueryEscape(res.Sample.ID), nil))
	if recDel.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", recDel.Code)
	}
	var cleared voice.SpeakerStatus
	if err := json.NewDecoder(recDel.Body).Decode(&cleared); err != nil {
		t.Fatalf("decode cleared status: %v", err)
	}
	if cleared.Enrolled || cleared.SampleCount != 0 {
		t.Fatalf("expected 0 samples after delete, got %+v", cleared)
	}
}

func TestEventHub_RoutesMessagesByClientDevice(t *testing.T) {
	hub := NewEventHub()
	pcCh := make(chan string, 10)
	androidCh := make(chan string, 10)
	hub.AddClientWithDevice(pcCh, "pc")
	hub.AddClientWithDevice(androidCh, "android")
	defer func() {
		hub.RemoveClient(pcCh)
		hub.RemoveClient(androidCh)
	}()

	hub.BroadcastToDevice("android", "only-for-phone")
	select {
	case msg := <-androidCh:
		if msg != "only-for-phone" {
			t.Fatalf("android got %q, want only-for-phone", msg)
		}
	default:
		t.Fatalf("expected android client to receive android-targeted message")
	}
	select {
	case leaked := <-pcCh:
		t.Fatalf("pc client must not receive android-targeted message, got %q", leaked)
	default:
	}

	hub.BroadcastToDevice("pc", "only-for-pc")
	select {
	case msg := <-pcCh:
		if msg != "only-for-pc" {
			t.Fatalf("pc got %q, want only-for-pc", msg)
		}
	default:
		t.Fatalf("expected pc client to receive pc-targeted message")
	}
	select {
	case leaked := <-androidCh:
		t.Fatalf("android client must not receive pc-targeted message, got %q", leaked)
	default:
	}
}

func TestServer_HandleHistoryIsolatesDeviceSTM(t *testing.T) {
	stm := memory.NewSTM(10)
	srv := &Server{stm: stm}
	srv.stmForDevice("pc").AddMessage(llm.Message{Role: llm.RoleUser, Content: "msg pc"})
	srv.stmForDevice("android").AddMessage(llm.Message{Role: llm.RoleUser, Content: "msg android"})

	recAndroid := httptest.NewRecorder()
	reqAndroid := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	reqAndroid.Header.Set("X-Pixel-Device", "android")
	srv.handleHistory(recAndroid, reqAndroid)
	if recAndroid.Code != http.StatusOK {
		t.Fatalf("android history status = %d", recAndroid.Code)
	}
	var androidMsgs []llm.Message
	if err := json.NewDecoder(recAndroid.Body).Decode(&androidMsgs); err != nil {
		t.Fatalf("decode android history: %v", err)
	}
	if len(androidMsgs) != 1 || androidMsgs[0].Content != "msg android" {
		t.Fatalf("unexpected android history: %+v", androidMsgs)
	}

	recPC := httptest.NewRecorder()
	reqPC := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	reqPC.Header.Set("X-Pixel-Device", "pc")
	srv.handleHistory(recPC, reqPC)
	if recPC.Code != http.StatusOK {
		t.Fatalf("pc history status = %d", recPC.Code)
	}
	var pcMsgs []llm.Message
	if err := json.NewDecoder(recPC.Body).Decode(&pcMsgs); err != nil {
		t.Fatalf("decode pc history: %v", err)
	}
	if len(pcMsgs) != 1 || pcMsgs[0].Content != "msg pc" {
		t.Fatalf("unexpected pc history: %+v", pcMsgs)
	}
}

func TestServer_ScheduledTaskBroadcastsOnlyToRequestingDevice(t *testing.T) {
	hub := NewEventHub()
	pcCh := make(chan string, 10)
	androidCh := make(chan string, 10)
	hub.AddClientWithDevice(pcCh, "pc")
	hub.AddClientWithDevice(androidCh, "android")

	stm := memory.NewSTM(10)
	sched := scheduler.NewScheduler()
	sched.SetBroadcaster(hub)

	done := make(chan struct{}, 2)
	sched.RegisterHandler("research_mock", func(ctx context.Context, task *scheduler.Task) error {
		reply := "Résultat pour " + task.TargetDevice
		scheduler.BroadcastForTask(hub, task, reply)
		scheduler.AddMessageForTask(stm, task, llm.Message{Role: llm.RoleAssistant, Content: reply})
		done <- struct{}{}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)

	sched.EnqueueWithDevice("research_mock", "Recherche phone", "q1", 0, "android")
	sched.TriggerDispatch()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for android task")
	}
	// Allow runTask post-handler logic to settle
	time.Sleep(100 * time.Millisecond)

	if len(androidCh) != 1 {
		t.Fatalf("expected exactly 1 SSE message on android, got %d", len(androidCh))
	}
	if got := <-androidCh; got != "Résultat pour android" {
		t.Fatalf("android got %q, want Résultat pour android", got)
	}
	if len(pcCh) != 0 {
		t.Fatalf("expected 0 SSE messages on pc for android task, got %d (%q)", len(pcCh), <-pcCh)
	}
	if len(stm.GetMessages()) != 0 {
		t.Fatalf("expected PC STM to stay empty, got %+v", stm.GetMessages())
	}
	if am := stm.ForDevice("android").GetMessages(); len(am) != 1 || am[0].Content != "Résultat pour android" {
		t.Fatalf("expected android STM to contain task reply, got %+v", am)
	}

	sched.EnqueueWithDevice("research_mock", "Recherche pc", "q2", 0, "pc")
	sched.TriggerDispatch()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pc task")
	}
	time.Sleep(100 * time.Millisecond)

	if len(pcCh) != 1 {
		t.Fatalf("expected exactly 1 SSE message on pc, got %d", len(pcCh))
	}
	if got := <-pcCh; got != "Résultat pour pc" {
		t.Fatalf("pc got %q, want Résultat pour pc", got)
	}
	if len(androidCh) != 0 {
		t.Fatalf("expected 0 SSE messages on android for pc task, got %d (%q)", len(androidCh), <-androidCh)
	}
}

func TestHandleBridgeSound(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	srv.handleBridgeSound(rec, httptest.NewRequest(http.MethodGet, "/api/sounds/bridge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sounds/bridge status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("Content-Type = %q, want audio/wav", ct)
	}
	if rec.Body.Len() <= 44 || string(rec.Body.Bytes()[:4]) != "RIFF" {
		t.Fatalf("expected valid WAV payload, got %d bytes", rec.Body.Len())
	}
}

func TestHandleMediaMix_TokenValidation(t *testing.T) {
	sessionID := "deadbeef1234"
	sessionDir := filepath.Join("/tmp/pixel_openaudio_mix", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("mkdir sessionDir: %v", err)
	}
	defer os.RemoveAll(sessionDir)

	if err := os.WriteFile(filepath.Join(sessionDir, "mix_token.txt"), []byte("secret-mix-sig"), 0644); err != nil {
		t.Fatalf("write mix_token: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "part_000.mp3"), []byte("ID3fakeaudio"), 0644); err != nil {
		t.Fatalf("write part_000.mp3: %v", err)
	}

	srv := &Server{}

	// Invalid signature rejected
	recBad := httptest.NewRecorder()
	srv.handleMediaMix(recBad, httptest.NewRequest(http.MethodGet, "/api/media/mix?session="+sessionID+"&file=part_000.mp3&sig=wrong", nil))
	if recBad.Code != http.StatusUnauthorized {
		t.Fatalf("invalid sig status = %d, want 401", recBad.Code)
	}

	// Path traversal rejected
	recTrav := httptest.NewRecorder()
	srv.handleMediaMix(recTrav, httptest.NewRequest(http.MethodGet, "/api/media/mix?session=../etc&file=part_000.mp3&sig=secret-mix-sig", nil))
	if recTrav.Code != http.StatusBadRequest {
		t.Fatalf("path traversal status = %d, want 400", recTrav.Code)
	}

	// Valid signature serves chunk
	recOK := httptest.NewRecorder()
	srv.handleMediaMix(recOK, httptest.NewRequest(http.MethodGet, "/api/media/mix?session="+sessionID+"&file=part_000.mp3&sig=secret-mix-sig", nil))
	if recOK.Code != http.StatusOK {
		t.Fatalf("valid sig status = %d, want 200", recOK.Code)
	}
	if ct := recOK.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("Content-Type = %q, want audio/mpeg", ct)
	}
}
