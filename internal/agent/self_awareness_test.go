package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/marce555/pixel/internal/memory"
)

func TestSelfAwarenessLedgerPersistence(t *testing.T) {
	testLedgerPath := "test_pixel_self_knowledge.json"
	defer os.Remove(testLedgerPath)

	coreMem := memory.NewCoreMemory("test_core_mem.json")
	defer os.Remove("test_core_mem.json")

	ltm := memory.NewLTM()
	thoughtStream := memory.NewThoughtStream(10)
	projectManager := memory.NewProjectManager("test_proj.mp")
	defer os.Remove("test_proj.mp")
	unconscious := memory.NewUnconsciousManager()

	agent := NewSelfAwarenessAgent(nil, coreMem, ltm, thoughtStream, projectManager, unconscious, testLedgerPath)

	// 1. Initial state
	ledger := agent.GetLedger()
	if ledger.TotalLearnings != 0 {
		t.Fatalf("expected 0 learnings initially, got %d", ledger.TotalLearnings)
	}

	// 2. Record learning
	event := SelfLearningEvent{
		Topic:      "Cryptographie Post-Quantique",
		Summary:    "Étude des réseaux euclidiens et du schéma Kyber pour résister aux attaques quantiques.",
		Source:     "curiosity",
		Category:   "Technical",
		Importance: 0.8,
		Tags:       []string{"crypto", "quantum", "security"},
	}

	err := agent.RecordLearning(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error recording learning: %v", err)
	}

	ledger = agent.GetLedger()
	if ledger.TotalLearnings != 1 {
		t.Fatalf("expected 1 learning, got %d", ledger.TotalLearnings)
	}
	if ledger.LearnedDomains["Technical"] != 1 {
		t.Fatalf("expected 1 Technical domain count, got %d", ledger.LearnedDomains["Technical"])
	}
	if len(ledger.RecentMilestones) != 1 {
		t.Fatalf("expected 1 milestone, got %d", len(ledger.RecentMilestones))
	}

	// 3. Reload from disk to verify persistence
	reloadedAgent := NewSelfAwarenessAgent(nil, coreMem, ltm, thoughtStream, projectManager, unconscious, testLedgerPath)
	reloadedLedger := reloadedAgent.GetLedger()
	if reloadedLedger.TotalLearnings != 1 {
		t.Fatalf("expected 1 learning after reload, got %d", reloadedLedger.TotalLearnings)
	}
	if reloadedLedger.Learnings[0].Topic != "Cryptographie Post-Quantique" {
		t.Fatalf("expected topic 'Cryptographie Post-Quantique', got '%s'", reloadedLedger.Learnings[0].Topic)
	}
}

func TestSelfAwarenessIntrospection(t *testing.T) {
	testLedgerPath := "test_pixel_self_knowledge_intro.json"
	defer os.Remove(testLedgerPath)

	coreMem := memory.NewCoreMemory("test_core_mem_intro.json")
	defer os.Remove("test_core_mem_intro.json")

	ltm := memory.NewLTM()
	thoughtStream := memory.NewThoughtStream(10)
	thoughtStream.AddThought("Je réfléchis à la convergence entre mécanique quantique et conscience.", []float32{0.1, 0.2})
	projectManager := memory.NewProjectManager("test_proj_intro.mp")
	defer os.Remove("test_proj_intro.mp")
	unconscious := memory.NewUnconsciousManager()

	agent := NewSelfAwarenessAgent(nil, coreMem, ltm, thoughtStream, projectManager, unconscious, testLedgerPath)

	// Record a learning event
	_ = agent.RecordLearning(context.Background(), SelfLearningEvent{
		Topic:      "Neuroplasticité Hippocampique",
		Summary:    "La neurogenèse persiste à l'âge adulte et joue un rôle dans la mémoire spatiale.",
		Source:     "curiosity",
		Category:   "Neuroscience",
		Importance: 0.9,
	})

	// Test introspection on identity
	introIdentity, err := agent.Introspect(context.Background(), "Qui es-tu vraiment ?")
	if err != nil {
		t.Fatalf("unexpected error on Introspect: %v", err)
	}
	if !strings.Contains(introIdentity, "CONSCIENCE DE SOI & AUTOCONNAISSANCE") {
		t.Errorf("expected header in introspection")
	}
	if !strings.Contains(introIdentity, "IDENTITÉ & NATURE PROFONDE") {
		t.Errorf("expected identity section in introspection")
	}
	if !strings.Contains(introIdentity, "PREMIÈRE PERSONNE") {
		t.Errorf("expected instruction for first-person expression")
	}

	// Test introspection on algorithms (Thalamus)
	introThalamus, err := agent.Introspect(context.Background(), "Comment fonctionne ton thalamus et ton inhibition ?")
	if err != nil {
		t.Fatalf("unexpected error on Introspect: %v", err)
	}
	if !strings.Contains(introThalamus, "ThalamicGate") {
		t.Errorf("expected ThalamicGate in introspection response")
	}
	if !strings.Contains(introThalamus, "Inhibition Latente") {
		t.Errorf("expected Inhibition Latente description")
	}

	// Test introspection on curiosity / learning
	introCuriosity, err := agent.Introspect(context.Background(), "Qu'as-tu appris récemment ?")
	if err != nil {
		t.Fatalf("unexpected error on Introspect: %v", err)
	}
	if !strings.Contains(introCuriosity, "Neuroplasticité Hippocampique") {
		t.Errorf("expected recorded learning to appear in introspection")
	}

	// Test IntrospectForCuriosity
	curiosityState := agent.IntrospectForCuriosity(context.Background())
	if !strings.Contains(curiosityState, "Neuroplasticité Hippocampique") {
		t.Errorf("expected learning in curiosity state")
	}
	if !strings.Contains(curiosityState, "mécanique quantique et conscience") {
		t.Errorf("expected recent thought in curiosity state")
	}
}

func TestDetectSelfAwarenessIntent(t *testing.T) {
	cases := []struct {
		input       string
		expected    bool
		expectedSub string
	}{
		{"Qui es-tu ?", true, "identity"},
		{"Dis-moi qui tu es vraiment", true, "identity"},
		{"Présente-toi s'il te plaît", true, "identity"},
		{"Quelle est ton architecture ?", true, "architecture"},
		{"Comment fonctionnent tes algorithmes ?", true, "architecture"},
		{"Comment fonctionne ton thalamus ?", true, "modules"},
		{"Comment dors-tu et comment se passe ta consolidation ?", true, "modules"},
		{"As-tu une conscience de soi ?", true, "interior"},
		{"Qu'as-tu appris aujourd'hui ?", true, "learnings"},
		{"Joue du Pink Floyd", false, ""},
		{"Quelle est la météo à Marseille ?", false, ""},
	}

	for _, c := range cases {
		ok, sub := detectSelfAwarenessIntent(c.input)
		if ok != c.expected {
			t.Errorf("input '%s': expected ok=%v, got %v", c.input, c.expected, ok)
		}
		if ok && c.expectedSub != "" && sub != c.expectedSub {
			t.Errorf("input '%s': expected sub=%s, got %s", c.input, c.expectedSub, sub)
		}
	}
}

func TestSuperiorAgentSelfAwarenessFastPath(t *testing.T) {
	coreMem := memory.NewCoreMemory("test_core_fastpath.json")
	defer os.Remove("test_core_fastpath.json")
	coreMem.UpdateStaticProfile(memory.StaticProfile{Name: "Marcelo", Role: "Utilisateur"})

	superior := NewSuperiorAgent(nil, coreMem, nil, nil, nil, nil, nil, nil, nil)
	resp := superior.analyzeQuery(context.Background(), "Quelle est ton architecture et tes algorithmes ?", nil)

	if resp.Action != "self_awareness" {
		t.Fatalf("expected action 'self_awareness', got '%s'", resp.Action)
	}
}

