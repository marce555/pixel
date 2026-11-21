package voice

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// AddressClassifier optionally disambiguates whether an ambiguous utterance
// from the verified owner is directed at Pixel or at someone else in the room.
type AddressClassifier interface {
	ClassifyAddressedToPixel(ctx context.Context, utterance string) (addressed bool, err error)
}

// AddressDecision captures whether a transcribed speech segment should trigger Pixel.
type AddressDecision struct {
	ShouldAccept bool   `json:"should_accept"`
	Intent       string `json:"address_intent"`
}

const activeDialogWindow = 45 * time.Second

var (
	wakeWordRegex = regexp.MustCompile(`(?i)\b(?:pixel|pixelle|pixels|picel|picsel|pixl|pixeul|pique[\s\-]*sel(?:le)?|pic[\s\-]*sel(?:le)?|pik[\s\-]*sel(?:le)?)\b`)

	explicitMediaCommandRegex = regexp.MustCompile(`(?i)\b(?:arrête|arrete|stop|coupe|pause|reprends|play|lance|mets|met|joue|suivante|prochaine|précédente|precedente|baisse|monte|volume|son)\b`)

	directAddressVerbRegex = regexp.MustCompile(`(?i)^(?:dis[\s,\-]+moi|donne[\s,\-]+moi|cherche|recherche|trouve|vérifie|verifie|regarde|résume|resume|explique|raconte|traduis|calcule|écris|ecris|envoie|publie|rappelle[\s,\-]+moi|lance|mets|met|joue|ouvre|ferme|active|désactive|desactive|arrête|arrete|stop|coupe|pause|baisse|monte|passe)\b`)

	interrogativeStartRegex = regexp.MustCompile(`(?i)^(?:qui|que|quoi|quel|quelle|quels|quelles|quand|où|ou|pourquoi|comment|combien|est[\s\-]ce\s+qu|qu[\'\x{2019}]est[\s\-]ce|peux[\s\-]tu|pourrais[\s\-]tu|as[\s\-]tu|veux[\s\-]tu|sais[\s\-]tu|tu\s+(?:peux|sais|connais|vois|as|veux))\b`)

	directPronounRegex = regexp.MustCompile(`(?i)\b(?:peux[\s\-]tu|pourrais[\s\-]tu|dis[\s\-]moi|donne[\s\-]moi|rappelle[\s\-]moi|aidez?[\s\-]moi|aide[\s\-]moi|tu\s+peux|tu\s+sais|tu\s+connais|t[\'\x{2019}]as|t[\'\x{2019}]es)\b`)

	leadFillerRegex = regexp.MustCompile(`(?i)^(?:euh|ben|bon|alors|dis|hé|hey|ok|voilà|du coup|attends)[\s,;:\-\.\!\?]+`)

	trivialBackchannelSet = map[string]bool{
		"oui": true, "non": true, "ok": true, "okay": true, "d'accord": true, "dac": true,
		"merci": true, "ah bon": true, "ah oui": true, "voilà": true, "voila": true,
		"c'est ça": true, "c'est ca": true, "attends": true, "pardon": true, "quoi": true,
		"euh": true, "hmm": true, "hm": true, "hum": true, "bon": true, "alors": true,
	}
)

// EvaluateSpokenTurn combines biometric speaker verification with lightweight conversational
// address gating so wake-wordless mode ignores TV, third parties, and side conversations.
func EvaluateSpokenTurn(
	ctx context.Context,
	transcript string,
	match SpeakerMatchResult,
	handsFreeNoWake bool,
	lastDialogActivity time.Time,
	classifier AddressClassifier,
) AddressDecision {
	cleaned := strings.TrimSpace(transcript)
	strippedPunct := strings.TrimSpace(strings.Trim(cleaned, ".,!?;:…\"'()[]{}- "))
	if strippedPunct == "" {
		return AddressDecision{ShouldAccept: false, Intent: "empty"}
	}

	// Explicit wake keyword is always accepted regardless of mode.
	if wakeWordRegex.MatchString(cleaned) {
		return AddressDecision{ShouldAccept: true, Intent: "wake_keyword"}
	}

	if !handsFreeNoWake {
		return AddressDecision{ShouldAccept: false, Intent: "wake_required"}
	}

	inActiveDialog := !lastDialogActivity.IsZero() && time.Since(lastDialogActivity) <= activeDialogWindow

	// 1. Biometric owner verification gate (when profile is enrolled & worker is active).
	if match.Available && match.Enrolled {
		if match.Reason == "too_short" {
			// Tolerate short explicit media commands ("stop", "pause", "suivante") or active follow-ups
			// when similarity already clears a slightly relaxed floor.
			nearThreshold := match.Score >= (match.Threshold - 0.06)
			if !(nearThreshold && (explicitMediaCommandRegex.MatchString(cleaned) || inActiveDialog)) {
				return AddressDecision{ShouldAccept: false, Intent: "too_short"}
			}
		} else if !match.IsOwner {
			return AddressDecision{ShouldAccept: false, Intent: "not_owner"}
		}
	}

	// 2. Conversational address gate: is the owner talking to Pixel or to someone else?
	lower := strings.ToLower(strippedPunct)
	core := strings.TrimSpace(leadFillerRegex.ReplaceAllString(lower, ""))
	if core == "" {
		core = lower
	}
	words := strings.Fields(core)

	if trivialBackchannelSet[core] {
		return AddressDecision{ShouldAccept: false, Intent: "trivial_backchannel"}
	}

	if inActiveDialog {
		if len(words) >= 2 || strings.Contains(cleaned, "?") || explicitMediaCommandRegex.MatchString(core) {
			return AddressDecision{ShouldAccept: true, Intent: "active_dialog"}
		}
		return AddressDecision{ShouldAccept: false, Intent: "trivial_backchannel"}
	}

	if strings.Contains(cleaned, "?") ||
		directAddressVerbRegex.MatchString(core) ||
		interrogativeStartRegex.MatchString(core) ||
		directPronounRegex.MatchString(core) {
		return AddressDecision{ShouldAccept: true, Intent: "direct_request"}
	}

	if len(words) <= 2 {
		if explicitMediaCommandRegex.MatchString(core) {
			return AddressDecision{ShouldAccept: true, Intent: "direct_request"}
		}
		return AddressDecision{ShouldAccept: false, Intent: "fragment_ignored"}
	}

	// 3. Ambiguous declarative 3+ word sentence: offload classification to fast local NPU.
	if classifier != nil {
		clsCtx, cancel := context.WithTimeout(ctx, 1800*time.Millisecond)
		defer cancel()
		addressed, err := classifier.ClassifyAddressedToPixel(clsCtx, cleaned)
		if err == nil {
			if addressed {
				return AddressDecision{ShouldAccept: true, Intent: "npu_assistant"}
			}
			return AddressDecision{ShouldAccept: false, Intent: "conversation_with_other"}
		}
	}

	return AddressDecision{ShouldAccept: false, Intent: "ambiguous_unconfirmed"}
}
