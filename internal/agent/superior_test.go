package agent

import (
	"context"
	"testing"

	"github.com/marce555/pixel/internal/llm"
)

func TestIsSongDuplicate(t *testing.T) {
	tests := []struct {
		candidate string
		existing  string
		expected  bool
	}{
		{
			candidate: "Los Cierra Bares - Desde tu ventana (Official)",
			existing:  "Los Cierra Bares - desde tu ventana (Official)",
			expected:  true,
		},
		{
			candidate: "Los Cierra Bares - Desde tu ventana",
			existing:  "Los Cierra Bares - desde tu ventana (Official Video)",
			expected:  true,
		},
		{
			candidate: "Desde tu ventana",
			existing:  "Los Cierra Bares - Desde tu ventana",
			expected:  true,
		},
		{
			candidate: "Soda Stereo - De Música Ligera",
			existing:  "Los Cierra Bares - Desde tu ventana",
			expected:  false,
		},
		{
			candidate: "Whitney Houston - I Wanna Dance with Somebody",
			existing:  "Madonna - Material Girl",
			expected:  false,
		},
		{
			candidate: "Los Prisioneros - Tren al sur",
			existing:  "Los Prisioneros - Tren al Sur (Remastered)",
			expected:  true,
		},
	}

	for _, tt := range tests {
		got := isSongDuplicate(tt.candidate, tt.existing)
		if got != tt.expected {
			t.Errorf("isSongDuplicate(%q, %q) = %v; want %v", tt.candidate, tt.existing, got, tt.expected)
		}
	}
}

func TestIsConversationalArticleQuestion(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		// Exact screenshot case: conversational question, NOT an order
		{
			input:    "Quels serait les articles que tu voudrais publier. Vers quoi vont tes recherches actuelles?",
			expected: true,
		},
		{
			input:    "Tu as déjà publié des articles ?",
			expected: true,
		},
		{
			input:    "C'est quoi un bon article selon toi ?",
			expected: true,
		},
		{
			input:    "Sur quoi portent tes recherches actuelles ?",
			expected: true,
		},
		{
			input:    "Quels articles aimerais-tu écrire ?",
			expected: true,
		},
		{
			input:    "Est-ce que tu as des idées d'articles ?",
			expected: true,
		},
		// Imperative orders: NOT conversational questions
		{
			input:    "Rédige un article sur la fusion nucléaire",
			expected: false,
		},
		{
			input:    "Publie un article sur AppliYou à propos des qubits",
			expected: false,
		},
		{
			input:    "Écris un article sur l'architecture RISC-V",
			expected: false,
		},
		{
			input:    "Crée un article sur les nouveaux modèles d'IA",
			expected: false,
		},
	}

	for _, tt := range tests {
		got := isConversationalArticleQuestion(tt.input)
		if got != tt.expected {
			t.Errorf("isConversationalArticleQuestion(%q) = %v; want %v", tt.input, got, tt.expected)
		}
	}
}

func TestIsSuspiciousTopic(t *testing.T) {
	tests := []struct {
		topic    string
		expected bool // true = rejected / suspicious
	}{
		// The exact buggy extracted topic from the screenshot
		{
			topic:    "s que tu voudrais publier. Vers quoi vont tes recherches actuelles",
			expected: true,
		},
		{
			topic:    "tu",
			expected: true,
		},
		{
			topic:    "es que tu penses ?",
			expected: true,
		},
		{
			topic:    "pourquoi pas",
			expected: true,
		},
		{
			topic:    "articles que tu veux",
			expected: true,
		},
		// Legitimate, clean topics
		{
			topic:    "La fusion nucléaire et les réacteurs Tokamak",
			expected: false,
		},
		{
			topic:    "Les ordinateurs quantiques supraconducteurs",
			expected: false,
		},
		{
			topic:    "Architecture RISC-V et microprocesseurs ouverts",
			expected: false,
		},
	}

	for _, tt := range tests {
		got := isSuspiciousTopic(tt.topic)
		if got != tt.expected {
			t.Errorf("isSuspiciousTopic(%q) = %v; want %v", tt.topic, got, tt.expected)
		}
	}
}

func TestArticleProposalAndConfirmation(t *testing.T) {
	assistantMsg := "Je peux rédiger et publier un article complet sur **La fusion nucléaire et les réacteurs Tokamak** sur AppliYou.fr. 🚀\n\nSouhaites-tu que je lance la rédaction en arrière-plan ?"

	if !isArticleProposal(assistantMsg) {
		t.Errorf("Expected assistant message to be recognized as an article proposal")
	}

	topic := extractProposedArticleTopic(assistantMsg)
	expectedTopic := "La fusion nucléaire et les réacteurs Tokamak"
	if topic != expectedTopic {
		t.Errorf("extractProposedArticleTopic = %q; want %q", topic, expectedTopic)
	}

	confirmations := []string{"oui", "vas-y", "d'accord", "lance", "ok", "confirme", "c'est parti", "oui s'il te plaît"}
	for _, c := range confirmations {
		if !isAffirmativeConfirmation(c) {
			t.Errorf("Expected %q to be affirmative confirmation", c)
		}
	}

	rejections := []string{"non", "pas maintenant", "attends", "annule", "parle-moi d'autre chose"}
	for _, r := range rejections {
		if isAffirmativeConfirmation(r) {
			t.Errorf("Did not expect %q to be affirmative confirmation", r)
		}
	}
}

type mockPublishClassifierProvider struct {
	response string
}

func (m *mockPublishClassifierProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	return m.response, nil
}

func (m *mockPublishClassifierProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- m.response
	close(out)
	close(errs)
	return out, errs
}

func (m *mockPublishClassifierProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestDetectPublishIntentWithLLM(t *testing.T) {
	ctx := context.Background()

	// 1. Screenshot case: Conversational question should be rejected immediately
	prov := &mockPublishClassifierProvider{
		response: `{"intent": "conversation", "topic": "", "confidence": 0.99}`,
	}
	agent := &SuperiorAgent{llmProvider: prov}

	ok, topic, shouldAsk := agent.detectPublishIntent(ctx, "Quels serait les articles que tu voudrais publier. Vers quoi vont tes recherches actuelles?", nil)
	if ok {
		t.Errorf("Expected false for conversational question, got ok=%v, topic=%q", ok, topic)
	}

	// 2. Explicit order: Should be classified as creer_article and ask for confirmation
	prov.response = `{"intent": "creer_article", "topic": "La fusion nucléaire et les réacteurs Tokamak", "confidence": 0.96}`
	ok, topic, shouldAsk = agent.detectPublishIntent(ctx, "Rédige un article sur la fusion nucléaire", nil)
	if !ok {
		t.Fatalf("Expected ok=true for explicit order, got false")
	}
	if topic != "La fusion nucléaire et les réacteurs Tokamak" {
		t.Errorf("Unexpected topic: %q", topic)
	}
	if !shouldAsk {
		t.Errorf("Expected shouldAskConfirmation=true")
	}

	// 3. Suspicious chopped topic from LLM should be rejected
	prov.response = `{"intent": "creer_article", "topic": "s que tu voudrais publier. Vers quoi vont tes recherches actuelles", "confidence": 0.95}`
	ok, _, _ = agent.detectPublishIntent(ctx, "Rédige un article", nil)
	if ok {
		t.Errorf("Expected suspicious topic to be rejected, got ok=true")
	}

	// 4. Low confidence should be rejected
	prov.response = `{"intent": "creer_article", "topic": "Informatique", "confidence": 0.60}`
	ok, _, _ = agent.detectPublishIntent(ctx, "Rédige un article", nil)
	if ok {
		t.Errorf("Expected low confidence to be rejected, got ok=true")
	}
}
