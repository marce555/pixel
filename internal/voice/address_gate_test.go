package voice

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubAddressClassifier struct {
	addressed bool
	err       error
	calls     int
}

func (s *stubAddressClassifier) ClassifyAddressedToPixel(_ context.Context, _ string) (bool, error) {
	s.calls++
	return s.addressed, s.err
}

func TestEvaluateSpokenTurn_ModesAndGating(t *testing.T) {
	ctx := context.Background()
	ownerMatch := SpeakerMatchResult{Available: true, Enrolled: true, IsOwner: true, Score: 0.78, Threshold: 0.60, Reason: "ok"}
	strangerMatch := SpeakerMatchResult{Available: true, Enrolled: true, IsOwner: false, Score: 0.34, Threshold: 0.60, Reason: "below_threshold"}
	shortMediaMatch := SpeakerMatchResult{Available: true, Enrolled: true, IsOwner: false, Score: 0.56, Threshold: 0.60, Reason: "too_short"}

	// 1. Classic wake-keyword mode: requires "Pixel" even for owner, rejects non-wake.
	if d := EvaluateSpokenTurn(ctx, "Quel temps fait-il ?", ownerMatch, false, time.Time{}, nil); d.ShouldAccept || d.Intent != "wake_required" {
		t.Fatalf("expected wake_required when handsFreeNoWake=false, got %+v", d)
	}
	if d := EvaluateSpokenTurn(ctx, "Pixel quel temps fait-il", strangerMatch, false, time.Time{}, nil); !d.ShouldAccept || d.Intent != "wake_keyword" {
		t.Fatalf("expected explicit wake keyword to pass, got %+v", d)
	}

	// 2. Wake-free mode: rejects stranger/TV voice immediately without invoking NPU classifier.
	cls := &stubAddressClassifier{addressed: true}
	if d := EvaluateSpokenTurn(ctx, "Quel temps fait-il demain ?", strangerMatch, true, time.Time{}, cls); d.ShouldAccept || d.Intent != "not_owner" {
		t.Fatalf("expected stranger rejection, got %+v", d)
	}
	if cls.calls != 0 {
		t.Fatalf("classifier must not run when owner check fails")
	}

	// 3. Direct requests / questions from verified owner pass in 0ms without NPU classifier.
	for _, phrase := range []string{
		"Quel temps fait-il demain ?",
		"Lance-moi un mix de KDJ",
		"Tu peux vérifier mes mails ?",
		"Arrête la musique",
	} {
		d := EvaluateSpokenTurn(ctx, phrase, ownerMatch, true, time.Time{}, cls)
		if !d.ShouldAccept || d.Intent != "direct_request" {
			t.Fatalf("expected direct_request for %q, got %+v", phrase, d)
		}
	}
	if cls.calls != 0 {
		t.Fatalf("classifier should not run for clear direct requests, calls=%d", cls.calls)
	}

	// 4. Short media command tolerates too_short clip when score is near threshold.
	if d := EvaluateSpokenTurn(ctx, "Stop la musique", shortMediaMatch, true, time.Time{}, cls); !d.ShouldAccept || d.Intent != "direct_request" {
		t.Fatalf("expected near-threshold short media command tolerance, got %+v", d)
	}

	// 5. Trivial backchannels ignored even for owner outside active dialog.
	if d := EvaluateSpokenTurn(ctx, "Ah bon", ownerMatch, true, time.Time{}, cls); d.ShouldAccept || d.Intent != "trivial_backchannel" {
		t.Fatalf("expected trivial_backchannel rejection, got %+v", d)
	}

	// 6. Active dialog follow-up accepted cleanly without wake keyword.
	recent := time.Now().Add(-15 * time.Second)
	if d := EvaluateSpokenTurn(ctx, "Et pour demain matin", ownerMatch, true, recent, cls); !d.ShouldAccept || d.Intent != "active_dialog" {
		t.Fatalf("expected active_dialog follow-up acceptance, got %+v", d)
	}

	// 7. Ambiguous 3+ word side-conversation goes to NPU classifier.
	clsOther := &stubAddressClassifier{addressed: false}
	if d := EvaluateSpokenTurn(ctx, "Je passe prendre du pain tout à l'heure", ownerMatch, true, time.Time{}, clsOther); d.ShouldAccept || d.Intent != "conversation_with_other" {
		t.Fatalf("expected conversation_with_other rejection, got %+v", d)
	}
	if clsOther.calls != 1 {
		t.Fatalf("expected 1 NPU classifier call, got %d", clsOther.calls)
	}

	clsErr := &stubAddressClassifier{err: errors.New("timeout")}
	if d := EvaluateSpokenTurn(ctx, "Je passe prendre du pain tout à l'heure", ownerMatch, true, time.Time{}, clsErr); d.ShouldAccept || d.Intent != "ambiguous_unconfirmed" {
		t.Fatalf("expected conservative reject on classifier error, got %+v", d)
	}
}
