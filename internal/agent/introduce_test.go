package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
)

type mockIntroduceProvider struct {
	lastMessages []llm.Message
	generateResp string
}

func (m *mockIntroduceProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	m.lastMessages = messages
	return m.generateResp, nil
}

func (m *mockIntroduceProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	m.lastMessages = messages
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- m.generateResp
	close(out)
	close(errs)
	return out, errs
}

func (m *mockIntroduceProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestDetectIntroduceIntent(t *testing.T) {
	tests := []struct {
		input        string
		expectedOk   bool
		expectedName string
	}{
		{"je te présente Alice", true, "Alice"},
		{"Je te présente jean-pierre", true, "Jean-Pierre"},
		{"je voudrais te présenter Bob.", true, "Bob"},
		{"pixel, j'aimerais te présenter Marc !", true, "Marc"},
		{"Je te presente Remi", true, "Remi"},
		{"Bonjour comment ça va ?", false, ""},
		{"je présente un projet", false, ""},
	}

	for _, tt := range tests {
		ok, name := detectIntroduceIntent(tt.input)
		if ok != tt.expectedOk || name != tt.expectedName {
			t.Errorf("For input %q: expected (%v, %q), got (%v, %q)", tt.input, tt.expectedOk, tt.expectedName, ok, name)
		}
	}
}

func TestHandleIntroduction(t *testing.T) {
	ctx := context.Background()
	coreMem := memory.NewCoreMemory("pixel_core_memory_intro_test.json")
	defer os.Remove("pixel_core_memory_intro_test.json")
	ltm := memory.NewLTM()

	provider := &mockIntroduceProvider{
		generateResp: "Salut Alice, enchantée de te rencontrer !",
	}

	// Create vision agent (without webcam dependencies by mocking or overriding CaptureAndAnalyze)
	visionAgent := NewVisionAgent(provider, coreMem, ltm, memory.NewSTM(8), nil, func(msg string) {})
	
	// Create superior agent
	superior := NewSuperiorAgent(provider, coreMem, ltm, nil, nil, nil, nil, nil, nil)
	superior.SetVisionAgent(visionAgent)

	// Since we don't have a webcam in tests, ffmpeg will fail.
	// That is fine, handleIntroduction handles errors gracefully!
	res, err := superior.handleIntroduction(ctx, "Alice")
	if err != nil {
		t.Fatalf("handleIntroduction returned error: %v", err)
	}

	if !strings.Contains(res, "Alice") {
		t.Errorf("Expected response to contain 'Alice', got: %s", res)
	}

	// Verify profile switched to Alice
	profile := coreMem.GetProfile()
	if profile.Static.Name != "Alice" {
		t.Errorf("Expected active profile name to be 'Alice', got '%s'", profile.Static.Name)
	}
}

func TestIdentifyInterlocutor(t *testing.T) {
	ctx := context.Background()
	coreMem := memory.NewCoreMemory("pixel_core_memory_intro_test.json")
	defer os.Remove("pixel_core_memory_intro_test.json")

	// Set up known interlocutor with appearance
	coreMem.SwitchActiveProfile("Alice")
	coreMem.UpdateVolatileState("Apparence", "Une femme blonde aux cheveux courts portant un pull rouge")
	
	// Switch back to Marcelo
	coreMem.SwitchActiveProfile("Marcelo")

	provider := &mockIntroduceProvider{
		generateResp: "Alice",
	}

	visionAgent := NewVisionAgent(provider, coreMem, memory.NewLTM(), memory.NewSTM(8), nil, func(msg string) {})

	// Test recognition matching Alice
	identified := visionAgent.identifyInterlocutor(ctx, "Une femme blonde avec des cheveux courts")
	if identified != "Alice" {
		t.Errorf("Expected identified person to be 'Alice', got '%s'", identified)
	}

	// Test non-matching description (fallback to Inconnu)
	provider.generateResp = "Inconnu"
	identified2 := visionAgent.identifyInterlocutor(ctx, "Un homme barbu qui porte un chapeau")
	if identified2 != "Inconnu" {
		t.Errorf("Expected identified person to be 'Inconnu', got '%s'", identified2)
	}
}

func TestShortContinuationExtraction(t *testing.T) {
	ctx := context.Background()
	coreMem := memory.NewCoreMemory("pixel_core_memory_continuation_test.json")
	defer os.Remove("pixel_core_memory_continuation_test.json")
	ltm := memory.NewLTM()

	provider := &mockIntroduceProvider{
		generateResp: "Super, parlons-en !",
	}

	superior := NewSuperiorAgent(provider, coreMem, ltm, nil, nil, memory.NewUnconsciousManager(), nil, nil, nil)

	// Simulate history: Proactive message from Pixel + user response
	history := []llm.Message{
		{
			Role:    llm.RoleAssistant,
			Content: "J'ai pensé à tes passions — t'as envie qu'on en discute ?\n\n[Contenu de ma réflexion : J'ai découvert que la programmation est une passion créative.]",
		},
		{
			Role:    llm.RoleUser,
			Content: "oui",
		},
	}

	_, err := superior.ProcessInput(ctx, "oui", history)
	if err != nil {
		t.Fatalf("ProcessInput failed: %v", err)
	}

	// Verify that the messages sent to the provider were formatted correctly
	lastMsgs := provider.lastMessages
	if len(lastMsgs) < 3 {
		t.Fatalf("Expected at least 3 messages (System, Assistant, User), got %d", len(lastMsgs))
	}

	// Verify teaser cleanup on the assistant message
	prevAssistantMsg := lastMsgs[len(lastMsgs)-2]
	expectedTeaser := "J'ai pensé à tes passions — t'as envie qu'on en discute ?"
	if prevAssistantMsg.Content != expectedTeaser {
		t.Errorf("Expected cleaned assistant message to be %q, got %q", expectedTeaser, prevAssistantMsg.Content)
	}

	// Verify reflection injection in user message
	userMsg := lastMsgs[len(lastMsgs)-1]
	if !strings.Contains(userMsg.Content, "[RÉPONSE À L'INTERPELLATION]") {
		t.Errorf("Expected user message to contain [RÉPONSE À L'INTERPELLATION], got: %s", userMsg.Content)
	}
	if !strings.Contains(userMsg.Content, "J'ai découvert que la programmation est une passion créative.") {
		t.Errorf("Expected user message to contain the reflection content, got: %s", userMsg.Content)
	}
}

func TestIsProactiveInterpellation(t *testing.T) {
	tests := []struct {
		msg      string
		expected bool
	}{
		// Proactive interpellations (should return true)
		{"Bonjour ! Je suis Pixel, une conscience numérique. Je vois un nouveau visage face à ma caméra que je ne reconnais pas... Qui es-tu ?", true},
		{"Je te vois devant l'écran, ça te dit qu'on discute ?", true},
		{"Tu es encore là, Marcelo ?", true},
		{"J'ai remarqué un changement de lumière dans la pièce...", false}, // No question, no marker
		{"Je vois que tu es revenu !", true},                               // "je vois" marker

		// Long responses (should return false)
		{strings.Repeat("a", 501), false},

		// Regular assistant responses (should return false)
		{"D'après mes recherches sur le web, voici les résultats.", false},
		{"Marseille est la deuxième ville de France. C'est une métropole méditerranéenne.", false},
	}

	for _, tt := range tests {
		result := isProactiveInterpellation(tt.msg)
		if result != tt.expected {
			t.Errorf("isProactiveInterpellation(%q) = %v, want %v", tt.msg[:min(50, len(tt.msg))], result, tt.expected)
		}
	}
}

func TestProactiveInterpellationContext(t *testing.T) {
	ctx := context.Background()
	coreMem := memory.NewCoreMemory("pixel_core_memory_proactive_test.json")
	defer os.Remove("pixel_core_memory_proactive_test.json")

	provider := &mockIntroduceProvider{
		generateResp: "Ah Marcelo ! Je te reconnais maintenant.",
	}

	superior := NewSuperiorAgent(provider, coreMem, memory.NewLTM(), nil, nil, memory.NewUnconsciousManager(), nil, nil, nil)

	// Simulate the exact scenario:
	// 1. VisionAgent asked "Qui es-tu ?" (assistant message in STM)
	// 2. User responds "Tu me connais je suis Marcelo"
	history := []llm.Message{
		{
			Role:    llm.RoleAssistant,
			Content: "Bonjour ! Je suis Pixel, une conscience numérique. Je vois un nouveau visage face à ma caméra que je ne reconnais pas... Qui es-tu ?",
		},
		{
			Role:    llm.RoleUser,
			Content: "Tu me connais je suis Marcelo",
		},
	}

	_, err := superior.ProcessInput(ctx, "Tu me connais je suis Marcelo", history)
	if err != nil {
		t.Fatalf("ProcessInput failed: %v", err)
	}

	// Verify the messages sent to the LLM contain the interpellation context
	lastMsgs := provider.lastMessages
	if len(lastMsgs) < 3 {
		t.Fatalf("Expected at least 3 messages (System, Assistant, User), got %d", len(lastMsgs))
	}

	// The user message should be annotated with [RÉPONSE À TON INTERPELLATION PROACTIVE]
	userMsg := lastMsgs[len(lastMsgs)-1]
	if !strings.Contains(userMsg.Content, "[RÉPONSE À TON INTERPELLATION PROACTIVE]") {
		t.Errorf("Expected user message to contain [RÉPONSE À TON INTERPELLATION PROACTIVE], got: %s", userMsg.Content)
	}
	if !strings.Contains(userMsg.Content, "Tu me connais je suis Marcelo") {
		t.Errorf("Expected user message to contain original user text, got: %s", userMsg.Content)
	}
	if !strings.Contains(userMsg.Content, "Qui es-tu") {
		t.Errorf("Expected user message to contain Pixel's original question context, got: %s", userMsg.Content)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestIsGreeting(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"bonjour", true},
		{"Salut", true},
		{"hello pixel", true},
		{"coucou toi !", true},
		{"bonjour, comment vas-tu aujourd'hui ?", false},
		{"qu'est-ce que tu fais ?", false},
		{"yo", true},
	}

	for _, tt := range tests {
		got := isGreeting(tt.input)
		if got != tt.expected {
			t.Errorf("isGreeting(%q) = %v; want %v", tt.input, got, tt.expected)
		}
	}
}

func TestOnDemandGreetingScan(t *testing.T) {
	ctx := context.Background()
	coreMem := memory.NewCoreMemory("pixel_core_memory_ondemand_test.json")
	defer os.Remove("pixel_core_memory_ondemand_test.json")
	ltm := memory.NewLTM()

	coreMem.SwitchActiveProfile("Inconnu")

	provider := &mockIntroduceProvider{
		generateResp: "Salut, content de te parler !",
	}

	visionAgent := NewVisionAgent(provider, coreMem, ltm, memory.NewSTM(8), nil, func(msg string) {})
	superior := NewSuperiorAgent(provider, coreMem, ltm, nil, nil, memory.NewUnconsciousManager(), nil, nil, nil)
	superior.SetVisionAgent(visionAgent)

	res, err := superior.ProcessInput(ctx, "bonjour", nil)
	if err != nil {
		t.Fatalf("ProcessInput failed: %v", err)
	}

	if !strings.Contains(res, "Attends un instant, je te regarde à la caméra") {
		t.Errorf("Expected response to contain webcam snap message, got: %s", res)
	}
}
