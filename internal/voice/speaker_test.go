package voice

import (
	"context"
	"path/filepath"
	"testing"
)

type stubEmbeddingExtractor struct {
	ready   bool
	results map[string]EmbeddingResult
	errs    map[string]error
}

func (s *stubEmbeddingExtractor) IsReady(_ context.Context) bool {
	return s.ready
}

func (s *stubEmbeddingExtractor) ExtractEmbedding(_ context.Context, wavPath string) (EmbeddingResult, error) {
	if err, ok := s.errs[wavPath]; ok {
		if res, okRes := s.results[wavPath]; okRes {
			return res, err
		}
		return EmbeddingResult{}, err
	}
	if res, ok := s.results[wavPath]; ok {
		return res, nil
	}
	return EmbeddingResult{DurationMs: 1800, InferMs: 18, Embedding: []float64{1, 0, 0, 0}}, nil
}

func TestSpeakerVerifierEnrollVerifyAndReload(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "speaker_profile.json")

	ext := &stubEmbeddingExtractor{
		ready: true,
		results: map[string]EmbeddingResult{
			"pc_1.wav":      {Dim: 4, DurationMs: 2100, InferMs: 19, Embedding: []float64{2.0, 0.2, 0.0, 0.0}}, // Unnormalized
			"android_1.wav": {Dim: 4, DurationMs: 2400, InferMs: 21, Embedding: []float64{0.9, 0.4, 0.0, 0.0}},
			"owner_pc.wav":  {Dim: 4, DurationMs: 1900, InferMs: 16, Embedding: []float64{0.96, 0.22, 0.0, 0.0}},
			"tv_voice.wav":  {Dim: 4, DurationMs: 2300, InferMs: 17, Embedding: []float64{0.0, 0.1, 1.0, 0.0}},
			"too_short.wav": {Dim: 4, DurationMs: 320, InferMs: 11, Embedding: []float64{0.95, 0.15, 0.0, 0.0}},
		},
	}

	sv := NewSpeakerVerifierWithExtractor(profilePath, ext)
	ctx := context.Background()

	// Uninitialized before enrollment
	initial := sv.VerifyWav(ctx, "owner_pc.wav")
	if initial.Enrolled || initial.IsOwner || initial.Reason != "not_enrolled" {
		t.Fatalf("expected not_enrolled before enrollment, got %+v", initial)
	}

	// Enroll PC sample + Android sample
	res1, err := sv.EnrollWav(ctx, "pc_1.wav", "pc", "Bonjour Pixel")
	if err != nil {
		t.Fatalf("enroll pc_1 failed: %v", err)
	}
	if res1.SampleCount != 1 {
		t.Fatalf("expected 1 sample, got %d", res1.SampleCount)
	}

	res2, err := sv.EnrollWav(ctx, "android_1.wav", "android", "Quel temps fait-il")
	if err != nil {
		t.Fatalf("enroll android_1 failed: %v", err)
	}
	if res2.SampleCount != 2 || res2.ConsistencyScore < 0.7 {
		t.Fatalf("expected consistent 2nd sample, got %+v", res2)
	}

	// Reload from disk and verify owner vs intruder
	reloaded := NewSpeakerVerifierWithExtractor(profilePath, ext)
	status := reloaded.GetStatus(ctx)
	if !status.Enrolled || status.SampleCount != 2 || !status.ObserveOnly {
		t.Fatalf("unexpected reloaded status: %+v", status)
	}

	ownerMatch := reloaded.VerifyWav(ctx, "owner_pc.wav")
	if !ownerMatch.IsOwner || ownerMatch.Reason != "ok" || ownerMatch.Score < 0.9 {
		t.Fatalf("expected owner match >= 0.9, got %+v", ownerMatch)
	}

	tvMatch := reloaded.VerifyWav(ctx, "tv_voice.wav")
	if tvMatch.IsOwner || tvMatch.Reason != "below_threshold" || tvMatch.Score > 0.25 {
		t.Fatalf("expected TV voice rejection, got %+v", tvMatch)
	}

	// Short clip should still report Score in observation mode, but flag too_short and not verify as owner
	shortMatch := reloaded.VerifyWav(ctx, "too_short.wav")
	if shortMatch.IsOwner || shortMatch.Reason != "too_short" || shortMatch.Score < 0.8 {
		t.Fatalf("expected too_short with observed score preserved, got %+v", shortMatch)
	}

	// Delete individual sample and clear profile
	if err := reloaded.DeleteSample(res1.Sample.ID); err != nil {
		t.Fatalf("delete sample failed: %v", err)
	}
	if st := reloaded.GetStatus(ctx); st.SampleCount != 1 {
		t.Fatalf("expected 1 sample after delete, got %d", st.SampleCount)
	}
	if err := reloaded.DeleteSample(""); err != nil {
		t.Fatalf("clear profile failed: %v", err)
	}
	if st := reloaded.GetStatus(ctx); st.Enrolled || st.SampleCount != 0 {
		t.Fatalf("expected empty profile after clear, got %+v", st)
	}
}

func TestSpeakerVerifierSettingsAndWorkerFallback(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "speaker_profile.json")
	ext := &stubEmbeddingExtractor{
		ready: false,
		errs: map[string]error{
			"short.wav": ErrAudioTooShort,
		},
	}

	sv := NewSpeakerVerifierWithExtractor(profilePath, ext)
	ctx := context.Background()

	// Unavailable worker should fail open gracefully without blocking callers
	res := sv.VerifyWav(ctx, "any.wav")
	if res.Available || res.Reason != "worker_unavailable" {
		t.Fatalf("expected worker_unavailable, got %+v", res)
	}

	// Threshold clamping + observe toggle
	enabled := false
	observe := false
	highThresh := 1.4
	if err := sv.UpdateSettings(&enabled, &observe, nil, &highThresh, nil); err != nil {
		t.Fatalf("update settings failed: %v", err)
	}
	st := sv.GetStatus(ctx)
	if st.Enabled || st.ObserveOnly || st.Threshold != 0.95 {
		t.Fatalf("unexpected clamped settings: %+v", st)
	}

	disabledRes := sv.VerifyWav(ctx, "any.wav")
	if disabledRes.Mode != "disabled" || disabledRes.Reason != "disabled" {
		t.Fatalf("expected disabled mode, got %+v", disabledRes)
	}

	// Re-enable and simulate ErrAudioTooShort from worker
	ext.ready = true
	sv.invalidateHealthCache()
	enabled = true
	noWake := true
	if err := sv.UpdateSettings(&enabled, nil, &noWake, nil, nil); err != nil {
		t.Fatalf("re-enable failed: %v", err)
	}
	if !sv.IsHandsFreeNoWake() {
		t.Fatalf("expected IsHandsFreeNoWake() == true after enabling handsFreeNoWake")
	}
	shortRes := sv.VerifyWav(ctx, "short.wav")
	if !shortRes.Available || shortRes.Reason != "too_short" {
		t.Fatalf("expected too_short from extractor error, got %+v", shortRes)
	}
}

func TestSpeakerVerifierPerDeviceCentroidAndQuota(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "speaker_profile.json")
	ext := &stubEmbeddingExtractor{
		ready: true,
		results: map[string]EmbeddingResult{
			"pc.wav":           {Dim: 4, DurationMs: 2200, InferMs: 14, Embedding: []float64{1.0, 0.0, 0.0, 0.0}},
			"android.wav":      {Dim: 4, DurationMs: 2200, InferMs: 14, Embedding: []float64{0.3, 0.95, 0.0, 0.0}},
			"android_live.wav": {Dim: 4, DurationMs: 2200, InferMs: 14, Embedding: []float64{0.32, 0.94, 0.0, 0.0}},
		},
	}

	sv := NewSpeakerVerifierWithExtractor(profilePath, ext)
	ctx := context.Background()

	// 10 PC samples should NOT pull down Android score once Android has its own centroid,
	// and >12 Android samples must not evict PC samples.
	for i := 0; i < 4; i++ {
		if _, err := sv.EnrollWav(ctx, "pc.wav", "pc-gui", "pc"); err != nil {
			t.Fatalf("enroll pc %d: %v", i, err)
		}
	}
	for i := 0; i < 14; i++ {
		if _, err := sv.EnrollWav(ctx, "android.wav", "android", "mob"); err != nil {
			t.Fatalf("enroll android %d: %v", i, err)
		}
	}

	st := sv.GetStatus(ctx)
	pcCount, androidCount := 0, 0
	for _, s := range st.Samples {
		if s.Device == "pc-gui" {
			pcCount++
		} else if s.Device == "android" {
			androidCount++
		}
	}
	if pcCount != 4 || androidCount != defaultMaxSamplesPerDevice {
		t.Fatalf("expected pc=4 android=%d, got pc=%d android=%d", defaultMaxSamplesPerDevice, pcCount, androidCount)
	}

	androidMatch := sv.VerifyWav(ctx, "android_live.wav")
	if !androidMatch.IsOwner || androidMatch.CentroidScore < 0.98 {
		t.Fatalf("expected high per-device Android centroid score >= 0.98, got %+v", androidMatch)
	}

	// Empty Whisper silence must not overwrite last real speech observation.
	sv.RecordObservation(androidMatch, "Bonjour Pixel")
	sv.RecordObservation(SpeakerMatchResult{Available: true, Enrolled: true, Score: 0.21, Reason: "below_threshold"}, "...")
	if last := sv.GetStatus(ctx).LastMatch; last == nil || last.Score != androidMatch.Score {
		t.Fatalf("expected silence observation to be ignored, got %+v", last)
	}
}
