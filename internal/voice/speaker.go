package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// ErrAudioTooShort indicates the audio clip does not contain enough speech frames for reliable speaker verification.
	ErrAudioTooShort = errors.New("audio too short for speaker embedding")
	// ErrSpeakerWorkerUnavailable indicates the local speaker embedding worker could not be reached.
	ErrSpeakerWorkerUnavailable = errors.New("speaker verification worker unavailable")
)

const (
	defaultSpeakerThreshold     = 0.60
	defaultMinSpeechMs          = 650.0
	defaultMinEnrollSpeechMs    = 800.0
	defaultMaxSamplesPerDevice  = 12
	defaultMaxAudioSeconds      = 15.0
)

// EmbeddingResult holds the raw embedding and timing returned by the ONNX extractor.
type EmbeddingResult struct {
	Embedding  []float64 `json:"embedding"`
	Dim        int       `json:"dim"`
	DurationMs float64   `json:"duration_ms"`
	InferMs    float64   `json:"infer_ms"`
}

// EmbeddingExtractor abstracts speaker embedding extraction for worker/test decoupling.
type EmbeddingExtractor interface {
	IsReady(ctx context.Context) bool
	ExtractEmbedding(ctx context.Context, wavPath string) (EmbeddingResult, error)
}

// SpeakerSample represents a single enrolled voice clip for the owner profile.
type SpeakerSample struct {
	ID         string    `json:"id"`
	Device     string    `json:"device"`
	Phrase     string    `json:"phrase,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	DurationMs float64   `json:"duration_ms"`
	Embedding  []float64 `json:"embedding"`
}

// SpeakerSampleSummary exposes enrolled sample metadata without dumping raw embedding vectors.
type SpeakerSampleSummary struct {
	ID         string    `json:"id"`
	Device     string    `json:"device"`
	Phrase     string    `json:"phrase,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	DurationMs float64   `json:"duration_ms"`
}

// SpeakerProfile persists the owner voiceprint and verification settings.
type SpeakerProfile struct {
	Version         int             `json:"version"`
	OwnerName       string          `json:"owner_name"`
	Enabled         bool            `json:"enabled"`
	ObserveOnly     bool            `json:"observe_only"`
	HandsFreeNoWake bool            `json:"handsfree_no_wake"`
	Threshold       float64         `json:"threshold"`
	MinSpeechMs     float64         `json:"min_speech_ms"`
	Samples         []SpeakerSample `json:"samples"`
}

// SpeakerMatchResult reports the verification outcome for one audio clip.
type SpeakerMatchResult struct {
	Available       bool    `json:"available"`
	Enrolled        bool    `json:"enrolled"`
	SampleCount     int     `json:"sample_count"`
	IsOwner         bool    `json:"is_owner"`
	Score           float64 `json:"score"`
	CentroidScore   float64 `json:"centroid_score"`
	BestSampleScore float64 `json:"best_sample_score"`
	Threshold       float64 `json:"threshold"`
	DurationMs      float64 `json:"duration_ms"`
	InferMs         float64 `json:"infer_ms"`
	Mode            string  `json:"mode"`
	Reason          string  `json:"reason"`
	OwnerName       string  `json:"owner_name"`
	ObservedAt      string  `json:"observed_at,omitempty"`
	TranscriptHint  string  `json:"transcript_hint,omitempty"`
}

// SpeakerEnrollResult reports the result of adding a voice enrollment sample.
type SpeakerEnrollResult struct {
	Sample           SpeakerSampleSummary `json:"sample"`
	SampleCount      int                  `json:"sample_count"`
	ConsistencyScore float64              `json:"consistency_score"`
	InferMs          float64              `json:"infer_ms"`
}

// SpeakerStatus summarizes speaker verification readiness, settings, and enrolled samples.
type SpeakerStatus struct {
	Available       bool                   `json:"available"`
	Enabled         bool                   `json:"enabled"`
	ObserveOnly     bool                   `json:"observe_only"`
	HandsFreeNoWake bool                   `json:"handsfree_no_wake"`
	Threshold       float64                `json:"threshold"`
	MinSpeechMs     float64                `json:"min_speech_ms"`
	OwnerName       string                 `json:"owner_name"`
	Enrolled        bool                   `json:"enrolled"`
	SampleCount     int                    `json:"sample_count"`
	Samples         []SpeakerSampleSummary `json:"samples"`
	LastMatch       *SpeakerMatchResult    `json:"last_match,omitempty"`
}

// SpeakerVerifier manages local voiceprint enrollment and cosine-similarity verification.
type SpeakerVerifier struct {
	mu              sync.RWMutex
	profilePath     string
	profile         SpeakerProfile
	centroid        []float64
	deviceCentroids map[string][]float64
	lastMatch       *SpeakerMatchResult
	extractor       EmbeddingExtractor
	worker          *httpSpeakerWorker
	workerMu        sync.Mutex
	lastHealthAt    time.Time
	lastHealthy     bool
}

// NewSpeakerVerifier initializes a SpeakerVerifier backed by persistent JSON storage and the local Python ONNX worker.
func NewSpeakerVerifier(profilePath, binDir, workerScriptPath, endpoint string) *SpeakerVerifier {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = "http://127.0.0.1:8090"
	}
	w := newHTTPSpeakerWorker(binDir, workerScriptPath, endpoint)
	return newSpeakerVerifierWithExtractor(profilePath, w, w)
}

// NewSpeakerVerifierWithExtractor creates a SpeakerVerifier using a custom EmbeddingExtractor (ideal for unit tests).
func NewSpeakerVerifierWithExtractor(profilePath string, ext EmbeddingExtractor) *SpeakerVerifier {
	return newSpeakerVerifierWithExtractor(profilePath, ext, nil)
}

func newSpeakerVerifierWithExtractor(profilePath string, ext EmbeddingExtractor, worker *httpSpeakerWorker) *SpeakerVerifier {
	sv := &SpeakerVerifier{
		profilePath:     profilePath,
		extractor:       ext,
		worker:          worker,
		profile:         defaultSpeakerProfile(),
		deviceCentroids: make(map[string][]float64),
	}
	sv.loadLocked()
	return sv
}

func defaultSpeakerProfile() SpeakerProfile {
	return SpeakerProfile{
		Version:     1,
		OwnerName:   "Marcelo",
		Enabled:     true,
		ObserveOnly: true,
		Threshold:   defaultSpeakerThreshold,
		MinSpeechMs: defaultMinSpeechMs,
		Samples:     []SpeakerSample{},
	}
}

// EnsureWorker ensures the persistent ONNX speaker embedding microservice is running locally.
func (sv *SpeakerVerifier) EnsureWorker(ctx context.Context) error {
	if sv.worker == nil {
		return nil
	}
	return sv.worker.ensureRunning(ctx)
}

// Stop terminates the managed local worker process if started by this verifier.
func (sv *SpeakerVerifier) Stop() {
	if sv.worker != nil {
		sv.worker.stop()
	}
}

func (sv *SpeakerVerifier) isExtractorAvailable(ctx context.Context) bool {
	if sv.extractor == nil {
		return false
	}
	sv.workerMu.Lock()
	defer sv.workerMu.Unlock()

	if sv.lastHealthy && time.Since(sv.lastHealthAt) < 10*time.Second {
		return true
	}
	checkCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	ok := sv.extractor.IsReady(checkCtx)
	if !ok && sv.worker != nil {
		// Attempt lazy recovery if worker wasn't up yet.
		if err := sv.worker.ensureRunning(ctx); err == nil {
			ok = sv.extractor.IsReady(checkCtx)
		}
	}
	sv.lastHealthy = ok
	sv.lastHealthAt = time.Now()
	return ok
}

// VerifyWav computes speaker similarity against the enrolled owner profile.
func (sv *SpeakerVerifier) VerifyWav(ctx context.Context, wavPath string) SpeakerMatchResult {
	sv.mu.RLock()
	enabled := sv.profile.Enabled
	observeOnly := sv.profile.ObserveOnly
	threshold := sv.profile.Threshold
	minSpeechMs := sv.profile.MinSpeechMs
	ownerName := sv.profile.OwnerName
	sampleCount := len(sv.profile.Samples)
	centroid := append([]float64(nil), sv.centroid...)
	deviceCentroids := make(map[string][]float64, len(sv.deviceCentroids))
	for dev, c := range sv.deviceCentroids {
		deviceCentroids[dev] = append([]float64(nil), c...)
	}
	samples := make([]SpeakerSample, len(sv.profile.Samples))
	copy(samples, sv.profile.Samples)
	sv.mu.RUnlock()

	mode := computeMode(enabled, observeOnly, sampleCount)
	base := SpeakerMatchResult{
		Enrolled:    sampleCount > 0,
		SampleCount: sampleCount,
		Threshold:   threshold,
		Mode:        mode,
		OwnerName:   ownerName,
		ObservedAt:  time.Now().Format(time.RFC3339),
	}

	if !enabled {
		base.Reason = "disabled"
		return base
	}
	if !sv.isExtractorAvailable(ctx) {
		base.Reason = "worker_unavailable"
		return base
	}

	base.Available = true
	extRes, err := sv.extractor.ExtractEmbedding(ctx, wavPath)
	if err != nil {
		if errors.Is(err, ErrAudioTooShort) {
			base.DurationMs = extRes.DurationMs
			base.Reason = "too_short"
			return base
		}
		sv.invalidateHealthCache()
		base.Available = false
		base.Reason = "worker_error"
		return base
	}

	base.DurationMs = extRes.DurationMs
	base.InferMs = extRes.InferMs

	if sampleCount == 0 || len(centroid) == 0 {
		base.Reason = "not_enrolled"
		return base
	}

	vec, norm := l2Normalize(extRes.Embedding)
	if norm == 0 || len(vec) != len(centroid) {
		base.Reason = "dim_mismatch"
		return base
	}

	// Score against each device centroid (e.g. pc-gui, android) so one device's mic timbre
	// does not dilute another device's centroid.
	bestDeviceCentroidSim := cosineSimilarityNormalized(vec, centroid)
	for _, devCentroid := range deviceCentroids {
		if len(devCentroid) != len(vec) {
			continue
		}
		if sim := cosineSimilarityNormalized(vec, devCentroid); sim > bestDeviceCentroidSim {
			bestDeviceCentroidSim = sim
		}
	}

	bestSim := -1.0
	for _, s := range samples {
		if len(s.Embedding) != len(vec) {
			continue
		}
		if sim := cosineSimilarityNormalized(vec, s.Embedding); sim > bestSim {
			bestSim = sim
		}
	}
	if bestSim < -0.99 {
		bestSim = bestDeviceCentroidSim
	}

	// Blend per-device centroid stability with best sample tolerance.
	combined := 0.65*bestDeviceCentroidSim + 0.35*bestSim
	base.Score = round3(combined)
	base.CentroidScore = round3(bestDeviceCentroidSim)
	base.BestSampleScore = round3(bestSim)

	if extRes.DurationMs > 0 && extRes.DurationMs < minSpeechMs {
		base.IsOwner = false
		base.Reason = "too_short"
		return base
	}

	if combined >= threshold {
		base.IsOwner = true
		base.Reason = "ok"
	} else {
		base.IsOwner = false
		base.Reason = "below_threshold"
	}
	return base
}

// RecordObservation saves a non-empty speech verification result as the latest observation for UI inspection.
func (sv *SpeakerVerifier) RecordObservation(res SpeakerMatchResult, transcript string) {
	clean := strings.TrimSpace(strings.Trim(transcript, ".,!?;:…- "))
	if clean == "" && res.Reason != "not_enrolled" {
		return
	}
	if len(clean) > 120 {
		clean = clean[:120] + "…"
	}
	res.TranscriptHint = clean
	sv.recordLastMatch(res)
}

// EnrollWav extracts an embedding from an audio sample and adds it to the owner profile.
func (sv *SpeakerVerifier) EnrollWav(ctx context.Context, wavPath, device, phrase string) (SpeakerEnrollResult, error) {
	if !sv.isExtractorAvailable(ctx) {
		return SpeakerEnrollResult{}, ErrSpeakerWorkerUnavailable
	}

	extRes, err := sv.extractor.ExtractEmbedding(ctx, wavPath)
	if err != nil {
		return SpeakerEnrollResult{}, err
	}
	if extRes.DurationMs > 0 && extRes.DurationMs < defaultMinEnrollSpeechMs {
		return SpeakerEnrollResult{}, fmt.Errorf("%w: %.0f ms (minimum %.0f ms requis pour l'enrôlement)", ErrAudioTooShort, extRes.DurationMs, defaultMinEnrollSpeechMs)
	}

	vec, norm := l2Normalize(extRes.Embedding)
	if norm == 0 {
		return SpeakerEnrollResult{}, fmt.Errorf("vecteur vocal invalide (norme nulle)")
	}

	device = strings.ToLower(strings.TrimSpace(device))
	if device == "" {
		device = "web"
	}
	phrase = strings.TrimSpace(phrase)

	sv.mu.Lock()
	defer sv.mu.Unlock()

	consistency := 1.0
	if devCentroid, ok := sv.deviceCentroids[device]; ok && len(devCentroid) == len(vec) {
		consistency = round3(cosineSimilarityNormalized(vec, devCentroid))
	} else if len(sv.centroid) == len(vec) {
		consistency = round3(cosineSimilarityNormalized(vec, sv.centroid))
	} else if len(sv.profile.Samples) > 0 && len(sv.profile.Samples[0].Embedding) != len(vec) {
		// Model dimension changed: cleanly reset legacy samples rather than corrupting centroid.
		sv.profile.Samples = nil
	}

	sample := SpeakerSample{
		ID:         fmt.Sprintf("spk_%d", time.Now().UnixNano()),
		Device:     device,
		Phrase:     phrase,
		CreatedAt:  time.Now(),
		DurationMs: round1(extRes.DurationMs),
		Embedding:  vec,
	}
	sv.profile.Samples = append(sv.profile.Samples, sample)

	// Enforce per-device quota so enrolling on smartphone never evicts PC samples (and vice versa).
	devCount := 0
	for _, s := range sv.profile.Samples {
		if normalizeDeviceKey(s.Device) == device {
			devCount++
		}
	}
	if devCount > defaultMaxSamplesPerDevice {
		toDrop := devCount - defaultMaxSamplesPerDevice
		kept := make([]SpeakerSample, 0, len(sv.profile.Samples)-toDrop)
		for _, s := range sv.profile.Samples {
			if toDrop > 0 && normalizeDeviceKey(s.Device) == device {
				toDrop--
				continue
			}
			kept = append(kept, s)
		}
		sv.profile.Samples = kept
	}

	sv.recomputeCentroidLocked()
	if err := sv.saveLocked(); err != nil {
		return SpeakerEnrollResult{}, err
	}

	return SpeakerEnrollResult{
		Sample: SpeakerSampleSummary{
			ID:         sample.ID,
			Device:     sample.Device,
			Phrase:     sample.Phrase,
			CreatedAt:  sample.CreatedAt,
			DurationMs: sample.DurationMs,
		},
		SampleCount:      len(sv.profile.Samples),
		ConsistencyScore: consistency,
		InferMs:          extRes.InferMs,
	}, nil
}

// UpdateSettings updates verification switches and threshold safely.
func (sv *SpeakerVerifier) UpdateSettings(enabled, observeOnly, handsFreeNoWake *bool, threshold *float64, ownerName *string) error {
	sv.mu.Lock()
	defer sv.mu.Unlock()

	if enabled != nil {
		sv.profile.Enabled = *enabled
	}
	if handsFreeNoWake != nil {
		sv.profile.HandsFreeNoWake = *handsFreeNoWake
		if observeOnly == nil {
			// Turning on wake-free mode switches out of passive observation automatically.
			sv.profile.ObserveOnly = !*handsFreeNoWake
		}
	}
	if observeOnly != nil {
		sv.profile.ObserveOnly = *observeOnly
	}
	if threshold != nil {
		t := *threshold
		if t < 0.30 {
			t = 0.30
		} else if t > 0.95 {
			t = 0.95
		}
		sv.profile.Threshold = round3(t)
	}
	if ownerName != nil && strings.TrimSpace(*ownerName) != "" {
		sv.profile.OwnerName = strings.TrimSpace(*ownerName)
	}
	return sv.saveLocked()
}

// IsHandsFreeNoWake reports whether wake-wordless voice signature activation is enabled.
func (sv *SpeakerVerifier) IsHandsFreeNoWake() bool {
	sv.mu.RLock()
	defer sv.mu.RUnlock()
	return sv.profile.Enabled && sv.profile.HandsFreeNoWake
}

// DeleteSample removes one enrolled sample by ID, or clears all samples when id is empty.
func (sv *SpeakerVerifier) DeleteSample(id string) error {
	sv.mu.Lock()
	defer sv.mu.Unlock()

	id = strings.TrimSpace(id)
	if id == "" {
		sv.profile.Samples = []SpeakerSample{}
		sv.centroid = nil
		sv.deviceCentroids = make(map[string][]float64)
		return sv.saveLocked()
	}

	filtered := make([]SpeakerSample, 0, len(sv.profile.Samples))
	found := false
	for _, s := range sv.profile.Samples {
		if s.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, s)
	}
	if !found {
		return fmt.Errorf("échantillon introuvable : %s", id)
	}
	sv.profile.Samples = filtered
	sv.recomputeCentroidLocked()
	return sv.saveLocked()
}

// GetStatus returns current profile metadata and worker readiness without exposing raw vectors.
func (sv *SpeakerVerifier) GetStatus(ctx context.Context) SpeakerStatus {
	avail := sv.isExtractorAvailable(ctx)

	sv.mu.RLock()
	defer sv.mu.RUnlock()

	summaries := make([]SpeakerSampleSummary, 0, len(sv.profile.Samples))
	for _, s := range sv.profile.Samples {
		summaries = append(summaries, SpeakerSampleSummary{
			ID:         s.ID,
			Device:     s.Device,
			Phrase:     s.Phrase,
			CreatedAt:  s.CreatedAt,
			DurationMs: s.DurationMs,
		})
	}
	var lastCopy *SpeakerMatchResult
	if sv.lastMatch != nil {
		cp := *sv.lastMatch
		lastCopy = &cp
	}

	return SpeakerStatus{
		Available:       avail,
		Enabled:         sv.profile.Enabled,
		ObserveOnly:     sv.profile.ObserveOnly,
		HandsFreeNoWake: sv.profile.HandsFreeNoWake,
		Threshold:       sv.profile.Threshold,
		MinSpeechMs:     sv.profile.MinSpeechMs,
		OwnerName:       sv.profile.OwnerName,
		Enrolled:        len(sv.profile.Samples) > 0,
		SampleCount:     len(sv.profile.Samples),
		Samples:         summaries,
		LastMatch:       lastCopy,
	}
}

func (sv *SpeakerVerifier) recordLastMatch(res SpeakerMatchResult) SpeakerMatchResult {
	sv.mu.Lock()
	cp := res
	sv.lastMatch = &cp
	sv.mu.Unlock()
	return res
}

func (sv *SpeakerVerifier) invalidateHealthCache() {
	sv.workerMu.Lock()
	sv.lastHealthy = false
	sv.workerMu.Unlock()
}

func normalizeDeviceKey(device string) string {
	d := strings.ToLower(strings.TrimSpace(device))
	if d == "" {
		return "web"
	}
	return d
}

func (sv *SpeakerVerifier) recomputeCentroidLocked() {
	sv.deviceCentroids = make(map[string][]float64)
	if len(sv.profile.Samples) == 0 {
		sv.centroid = nil
		return
	}
	dim := len(sv.profile.Samples[0].Embedding)
	if dim == 0 {
		sv.centroid = nil
		return
	}
	sum := make([]float64, dim)
	devSums := make(map[string][]float64)
	validCount := 0
	for _, s := range sv.profile.Samples {
		if len(s.Embedding) != dim {
			continue
		}
		dev := normalizeDeviceKey(s.Device)
		devSum, ok := devSums[dev]
		if !ok {
			devSum = make([]float64, dim)
			devSums[dev] = devSum
		}
		for i, v := range s.Embedding {
			sum[i] += v
			devSum[i] += v
		}
		validCount++
	}
	if validCount == 0 {
		sv.centroid = nil
		return
	}
	centroid, _ := l2Normalize(sum)
	sv.centroid = centroid
	for dev, devSum := range devSums {
		if dc, norm := l2Normalize(devSum); norm > 0 {
			sv.deviceCentroids[dev] = dc
		}
	}
}

func (sv *SpeakerVerifier) loadLocked() {
	sv.mu.Lock()
	defer sv.mu.Unlock()

	if strings.TrimSpace(sv.profilePath) == "" {
		return
	}
	raw, err := os.ReadFile(sv.profilePath)
	if err != nil {
		return
	}
	var loaded SpeakerProfile
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return
	}
	if loaded.OwnerName == "" {
		loaded.OwnerName = "Marcelo"
	}
	if loaded.Threshold <= 0 {
		loaded.Threshold = defaultSpeakerThreshold
	}
	if loaded.MinSpeechMs <= 0 {
		loaded.MinSpeechMs = defaultMinSpeechMs
	}
	for i := range loaded.Samples {
		normed, _ := l2Normalize(loaded.Samples[i].Embedding)
		loaded.Samples[i].Embedding = normed
	}
	sv.profile = loaded
	sv.recomputeCentroidLocked()
}

func (sv *SpeakerVerifier) saveLocked() error {
	if strings.TrimSpace(sv.profilePath) == "" {
		return nil
	}
	dir := filepath.Dir(sv.profilePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(sv.profile, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := sv.profilePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, sv.profilePath)
}

func computeMode(enabled, observeOnly bool, sampleCount int) string {
	if !enabled {
		return "disabled"
	}
	if sampleCount == 0 {
		return "uninitialized"
	}
	if observeOnly {
		return "observe"
	}
	return "enforce"
}

func l2Normalize(vec []float64) ([]float64, float64) {
	if len(vec) == 0 {
		return nil, 0
	}
	var sumSq float64
	for _, v := range vec {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, 0
		}
		sumSq += v * v
	}
	norm := math.Sqrt(sumSq)
	if norm <= 1e-12 {
		return nil, 0
	}
	out := make([]float64, len(vec))
	for i, v := range vec {
		out[i] = v / norm
	}
	return out, norm
}

func cosineSimilarityNormalized(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	if dot > 1.0 {
		return 1.0
	}
	if dot < -1.0 {
		return -1.0
	}
	return dot
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

// httpSpeakerWorker manages the local Python sherpa-onnx microservice lifecycle.
type httpSpeakerWorker struct {
	binDir           string
	workerScriptPath string
	endpoint         string
	host             string
	port             string
	client           *http.Client
	cmdMu            sync.Mutex
	cmd              *exec.Cmd
}

func newHTTPSpeakerWorker(binDir, workerScriptPath, endpoint string) *httpSpeakerWorker {
	u, err := url.Parse(endpoint)
	host := "127.0.0.1"
	port := "8090"
	if err == nil {
		if h := u.Hostname(); h != "" {
			host = h
		}
		if p := u.Port(); p != "" {
			port = p
		}
	}
	return &httpSpeakerWorker{
		binDir:           binDir,
		workerScriptPath: workerScriptPath,
		endpoint:         strings.TrimRight(endpoint, "/"),
		host:             host,
		port:             port,
		client:           &http.Client{Timeout: 8 * time.Second},
	}
}

func (w *httpSpeakerWorker) IsReady(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.endpoint+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (w *httpSpeakerWorker) ExtractEmbedding(ctx context.Context, wavPath string) (EmbeddingResult, error) {
	payload, err := json.Marshal(map[string]any{
		"wav_path":    wavPath,
		"max_seconds": defaultMaxAudioSeconds,
	})
	if err != nil {
		return EmbeddingResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint+"/embed", bytes.NewReader(payload))
	if err != nil {
		return EmbeddingResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return EmbeddingResult{}, fmt.Errorf("%w: %v", ErrSpeakerWorkerUnavailable, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return EmbeddingResult{}, err
	}

	if resp.StatusCode == http.StatusUnprocessableEntity {
		var errPayload struct {
			Error      string  `json:"error"`
			DurationMs float64 `json:"duration_ms"`
		}
		_ = json.Unmarshal(body, &errPayload)
		if errPayload.Error == "audio_too_short" {
			return EmbeddingResult{DurationMs: errPayload.DurationMs}, ErrAudioTooShort
		}
		return EmbeddingResult{DurationMs: errPayload.DurationMs}, fmt.Errorf("speaker worker 422: %s", errPayload.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return EmbeddingResult{}, fmt.Errorf("speaker worker error %d: %s", resp.StatusCode, string(body))
	}

	var out EmbeddingResult
	if err := json.Unmarshal(body, &out); err != nil {
		return EmbeddingResult{}, err
	}
	return out, nil
}

func (w *httpSpeakerWorker) ensureRunning(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 350*time.Millisecond)
	ready := w.IsReady(checkCtx)
	cancel()
	if ready {
		return nil
	}

	w.cmdMu.Lock()
	defer w.cmdMu.Unlock()

	checkCtx2, cancel2 := context.WithTimeout(ctx, 350*time.Millisecond)
	ready2 := w.IsReady(checkCtx2)
	cancel2()
	if ready2 {
		return nil
	}

	pythonPath := filepath.Join(w.binDir, "speaker", "venv", "bin", "python")
	modelPath := filepath.Join(w.binDir, "speaker", "model.onnx")
	if _, err := os.Stat(pythonPath); err != nil {
		return fmt.Errorf("python venv introuvable à %s: %w", pythonPath, err)
	}
	if _, err := os.Stat(modelPath); err != nil {
		return fmt.Errorf("modèle ONNX introuvable à %s: %w", modelPath, err)
	}
	if _, err := os.Stat(w.workerScriptPath); err != nil {
		return fmt.Errorf("script worker introuvable à %s: %w", w.workerScriptPath, err)
	}

	cmd := exec.Command(pythonPath, w.workerScriptPath, "--model", modelPath, "--host", w.host, "--port", w.port, "--threads", "2")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("échec démarrage speaker worker: %w", err)
	}
	w.cmd = cmd
	go func() {
		_ = cmd.Wait()
	}()

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
		hCtx, hCancel := context.WithTimeout(ctx, 300*time.Millisecond)
		up := w.IsReady(hCtx)
		hCancel()
		if up {
			return nil
		}
	}
	return fmt.Errorf("timeout démarrage speaker worker sur %s", w.endpoint)
}

func (w *httpSpeakerWorker) stop() {
	w.cmdMu.Lock()
	defer w.cmdMu.Unlock()
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
		w.cmd = nil
	}
}
