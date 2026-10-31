package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
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

func TestShouldTriggerRAG(t *testing.T) {
	agent := &SuperiorAgent{}

	tests := []struct {
		input    string
		action   string
		query    string
		expected bool
	}{
		// Screenshot case with typo "souvennirs": must trigger RAG!
		{
			input:    "Vraiment tu n'as aucun souvennirs de théologie ?",
			action:   "self_awareness", // even if router mistakenly returns self_awareness!
			query:    "",
			expected: true,
		},
		{
			input:    "Quels sont tes souvenirs concernant la theologie?",
			action:   "none",
			query:    "",
			expected: true,
		},
		{
			input:    "Tu te rappelles de Balthasar ?",
			action:   "none",
			query:    "",
			expected: true,
		},
		{
			input:    "Comment fonctionnent tes algorithmes ?",
			action:   "self_awareness",
			query:    "Comment fonctionnent tes algorithmes ?",
			expected: false,
		},
		{
			input:    "Qui es-tu ?",
			action:   "self_awareness",
			query:    "Qui es-tu ?",
			expected: false,
		},
	}

	for _, tt := range tests {
		got := agent.shouldTriggerRAG(tt.input, tt.action, tt.query)
		if got != tt.expected {
			t.Errorf("shouldTriggerRAG(%q, %q, %q) = %v; want %v", tt.input, tt.action, tt.query, got, tt.expected)
		}
	}
}

func TestAnalyzeQueryServerDiagnosticFastPath(t *testing.T) {
	agent := &SuperiorAgent{}
	ctx := context.Background()

	tests := []struct {
		input       string
		wantAction  string
		wantQuery   string
	}{
		{
			input:      "Pixel, fais un diagnostic du serveur 87.106.120.28",
			wantAction: "skill_ssh_server_health",
			wantQuery:  "root@87.106.120.28",
		},
		{
			input:      "check le serveur root@87.106.120.28",
			wantAction: "skill_ssh_server_health",
			wantQuery:  "root@87.106.120.28",
		},
		{
			input:      "diagnostique 87.106.120.28",
			wantAction: "skill_ssh_server_health",
			wantQuery:  "root@87.106.120.28",
		},
		{
			input:      "Pixel, fais un diagnostic du serveur",
			wantAction: "skill_check_appliyou_logs",
			wantQuery:  "",
		},
		{
			input:      "logs d'appliyou",
			wantAction: "skill_check_appliyou_logs",
			wantQuery:  "",
		},
	}

	for _, tt := range tests {
		resp := agent.analyzeQuery(ctx, tt.input, nil)
		if resp.Action != tt.wantAction {
			t.Errorf("analyzeQuery(%q) Action = %q, want %q", tt.input, resp.Action, tt.wantAction)
		}
		if resp.Query != tt.wantQuery {
			t.Errorf("analyzeQuery(%q) Query = %q, want %q", tt.input, resp.Query, tt.wantQuery)
		}
	}
}

func TestExtractBillboardRank(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"fais-moi écouter la 10e place de la Billboard son de 1990", 10},
		{"fais-moi écouter la 10ème place du billboard", 10},
		{"mets la dixième place du billboard 1983", 10},
		{"joue le numéro 5 du billboard", 5},
		{"fais-moi écouter la 20e place", 20},
		{"mets la place 7 du billboard hot 100", 7},
		{"joue le 3ème titre", 3},
		{"mets la deuxième place", 2},
		{"joue le numéro 1", 1},
		{"fais écouter le premier titre", 1},
		{"joue la radio billboard hot 100", 0},
		{"lance la radio billboard", 0},
		{"donne-moi la liste des 10 titres de la billboard", 0},
	}

	for _, tt := range tests {
		got := extractBillboardRank(tt.input)
		if got != tt.want {
			t.Errorf("extractBillboardRank(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestDetectMediaControlIntent(t *testing.T) {
	tests := []struct {
		input      string
		wantCmd    string
		wantMatch  bool
	}{
		// User's exact case
		{"passe au suivant", "next", true},
		{"passe à la suivante", "next", true},
		{"passe a la suivante", "next", true},
		{"au suivant", "next", true},
		{"à la suivante", "next", true},
		{"titre suivant", "next", true},
		{"morceau suivant", "next", true},
		{"chanson suivante", "next", true},
		{"musique suivante", "next", true},
		{"piste suivante", "next", true},
		{"passe au morceau suivant", "next", true},
		{"passe au titre suivant", "next", true},
		{"passe à la musique suivante", "next", true},
		{"passe la musique", "next", true},
		{"passe le morceau", "next", true},
		{"passe le titre", "next", true},
		{"passe la chanson", "next", true},
		{"passe ce morceau", "next", true},
		{"passe cette chanson", "next", true},
		{"passe ça", "next", true},
		{"passe", "next", true},
		{"skip", "next", true},
		{"next", "next", true},
		{"suivant", "next", true},
		{"suivante", "next", true},
		{"chanson d'après", "next", true},
		{"morceau d'après", "next", true},
		{"titre d'après", "next", true},
		{"change de morceau", "next", true},
		{"change de chanson", "next", true},
		{"change de musique", "next", true},
		{"passe au suivant s'il te plaît", "next", true},
		{"pixel passe au suivant", "next", true},

		// Previous
		{"précédent", "previous", true},
		{"precedent", "previous", true},
		{"précédente", "previous", true},
		{"morceau précédent", "previous", true},
		{"titre précédent", "previous", true},
		{"chanson précédente", "previous", true},
		{"passe au précédent", "previous", true},
		{"previous", "previous", true},
		{"back", "previous", true},

		// Stop
		{"stop", "stop", true},
		{"arrête la musique", "stop", true},
		{"arrete la musique", "stop", true},
		{"coupe la musique", "stop", true},
		{"silence", "stop", true},

		// Pause
		{"pause", "pause", true},
		{"mets en pause", "pause", true},

		// Play / Resume
		{"play", "play", true},
		{"reprends la musique", "play", true},
		{"relance la musique", "play", true},

		// Non-control queries (should NOT match)
		{"qui est le suivant sur la liste ?", "", false},
		{"quel est le classement billboard ?", "", false},
		{"pourquoi la musique ne marche pas", "", false},
		{"comment fonctionne le lecteur", "", false},
	}

	for _, tt := range tests {
		cmd, ok := detectMediaControlIntent(tt.input)
		if ok != tt.wantMatch || cmd != tt.wantCmd {
			t.Errorf("detectMediaControlIntent(%q) = (%q, %v), want (%q, %v)", tt.input, cmd, ok, tt.wantCmd, tt.wantMatch)
		}
	}
}

func TestExtractBillboardLimit(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"fais-moi écouter les 10 titres des 1996 fais-moi", 10},
		{"mets le top 10 du billboard 1983", 10},
		{"lance les 5 premiers titres de 1984", 5},
		{"fais écouter les 20 meilleurs morceaux de 1990", 20},
		{"lance la radio billboard 1996", 0},
		{"mets la 10ème place du billboard", 0},
	}

	for _, tt := range tests {
		got := extractBillboardLimit(tt.input)
		if got != tt.want {
			t.Errorf("extractBillboardLimit(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestFormatCleanTrackName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"📻 Billboard Hot 100 (1996) #2 : Mariah Carey and Boyz II Men - One Sweet Day lyrics", "Mariah Carey and Boyz II Men - One Sweet Day"},
		{"Lecture de : Daniel Powter - Bad Day lyrics", "Daniel Powter - Bad Day"},
		{"Radio Auto : Adele - Rolling in the Deep", "Adele - Rolling in the Deep"},
	}

	for _, tt := range tests {
		got := formatCleanTrackName(tt.input)
		if got != tt.want {
			t.Errorf("formatCleanTrackName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSocialGreetingAndHumanInteraction(t *testing.T) {
	ctx := context.Background()
	coreMem := memory.NewCoreMemory("")
	agent := &SuperiorAgent{
		coreMemory:  coreMem,
		unconscious: memory.NewUnconsciousManager(),
	}

	socialInputs := []string{
		"dis bonjour à Lisa qui est là",
		"alors dis bonjour à Lisa",
		"tu veux pas les dire autre chose juste bonjour C'est un peu court non",
		"passe le bonjour à Rémi",
		"fais un coucou à Lisa",
		"salue Laurent de ma part",
		"souhaite une bonne soirée à Lisa",
	}

	for _, inp := range socialInputs {
		if !isSocialOrThirdPartyInteraction(inp) {
			t.Errorf("isSocialOrThirdPartyInteraction(%q) = false, want true", inp)
		}
		if !isConversationalFastPath(inp) {
			t.Errorf("isConversationalFastPath(%q) = false, want true", inp)
		}
		if isShortContinuation(inp) {
			t.Errorf("isShortContinuation(%q) = true, want false", inp)
		}
		if needsReflection(inp, "none") {
			t.Errorf("needsReflection(%q) = true, want false", inp)
		}
		if ok, _ := detectFaceDiscoveryIntent(inp, "Marcelo"); ok {
			t.Errorf("detectFaceDiscoveryIntent(%q) = true, want false (should not trigger camera on social greeting)", inp)
		}
		routerResp := agent.analyzeQuery(ctx, inp, nil)
		if routerResp.Action != "none" {
			t.Errorf("analyzeQuery(%q).Action = %q, want \"none\"", inp, routerResp.Action)
		}
	}

	// Verify genuine camera / identity questions still trigger detectFaceDiscoveryIntent
	cameraQuestions := []string{
		"qui est là ?",
		"regarde qui est là",
		"tu me reconnais ?",
		"qui suis-je ?",
	}
	for _, q := range cameraQuestions {
		if ok, _ := detectFaceDiscoveryIntent(q, "Marcelo"); !ok {
			t.Errorf("detectFaceDiscoveryIntent(%q) = false, want true", q)
		}
	}

	// Verify buildSystemPrompt does NOT forbid saying Bonjour when user asks to greet Lisa
	history := []llm.Message{
		{Role: llm.RoleAssistant, Content: "Salut Marcelo ! Content de te revoir."},
		{Role: llm.RoleUser, Content: "dis bonjour à Lisa qui est là"},
	}
	sysPrompt := agent.buildSystemPrompt(history, "")
	if strings.Contains(sysPrompt, "NE DIS PAS BONJOUR") {
		t.Errorf("buildSystemPrompt should NOT contain 'NE DIS PAS BONJOUR' when user asks to greet Lisa")
	}
	if !strings.Contains(sysPrompt, "[INTERACTION HUMAINE & SALUTATION SOCIALE EN DIRECT") {
		t.Errorf("buildSystemPrompt should contain '[INTERACTION HUMAINE & SALUTATION SOCIALE EN DIRECT' when user asks to greet Lisa")
	}
}


