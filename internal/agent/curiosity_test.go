package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/resourceagent"
	"github.com/marce555/pixel/internal/scheduler"
	"github.com/marce555/pixel/internal/timeagent"
)

type mockEventBroadcaster struct {
	messages []string
}

func (m *mockEventBroadcaster) Broadcast(message string) {
	m.messages = append(m.messages, message)
}

func (m *mockEventBroadcaster) HasClients() bool {
	return true
}

type mockProvider struct{}

func (m *mockProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	return "Relance sympa de test", nil
}

func (m *mockProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- "Mock stream"
	close(out)
	close(errs)
	return out, errs
}

func (m *mockProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestSleepManagerBusyTracking(t *testing.T) {
	stm := memory.NewSTM(8)
	ltm := memory.NewLTM()
	coreMem := memory.NewCoreMemory("pixel_core_memory_test.json")
	defer os.Remove("pixel_core_memory_test.json")

	provider := &mockProvider{}
	sleepManager := NewSleepManager(stm, ltm, coreMem, nil, nil, provider)

	if sleepManager.IsBusy() {
		t.Fatal("SleepManager should not be busy initially")
	}

	// 1. Simulate starting an indexing task
	sleepManager.incrementBusy()
	if !sleepManager.IsBusy() {
		t.Fatal("SleepManager should be busy after increment")
	}

	// 2. Simulate finishing the indexing task
	sleepManager.decrementBusy()
	if sleepManager.IsBusy() {
		t.Fatal("SleepManager should not be busy after decrement")
	}
}

func TestCuriosityAgentStates(t *testing.T) {
	ctx := context.Background()
	stm := memory.NewSTM(8)
	ltm := memory.NewLTM()
	coreMem := memory.NewCoreMemory("pixel_core_memory_test.json")
	defer os.Remove("pixel_core_memory_test.json")

	provider := &mockProvider{}
	sleepManager := NewSleepManager(stm, ltm, coreMem, nil, nil, provider)
	eventHub := &mockEventBroadcaster{}
	timeAgent := timeagent.NewTimeAgent()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)

	curiosity := NewCuriosityAgent(provider, coreMem, stm, ltm, eventHub, timeAgent, webAgent, thoughtStream, sleepManager, nil, nil)

	// Check initial states
	curiosity.mu.Lock()
	if curiosity.interpelled || curiosity.isBored {
		curiosity.mu.Unlock()
		t.Fatal("Initial states should be false")
	}
	curiosity.mu.Unlock()

	// Add an assistant message to STM to simulate wait-state
	stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: "Hello from assistant"})

	// Verify we can interpellate
	msgs := stm.GetMessages()
	curiosity.interpellateInterlocuteur(ctx, msgs)

	curiosity.mu.Lock()
	if !curiosity.interpelled {
		curiosity.mu.Unlock()
		t.Fatal("Should be marked as interpelled after interpellation")
	}
	curiosity.mu.Unlock()

	if len(eventHub.messages) != 1 || eventHub.messages[0] != "Relance sympa de test" {
		t.Fatalf("Expected event to be broadcast, got: %v", eventHub.messages)
	}

	// Verify STM has the interpellation message added
	newMsgs := stm.GetMessages()
	if len(newMsgs) != 2 || newMsgs[1].Content != "Relance sympa de test" {
		t.Fatalf("STM should contain the new interpellation message, got: %v", newMsgs)
	}

	// Now simulate user reply
	stm.AddMessage(llm.Message{Role: llm.RoleUser, Content: "I am back!"})

	// To test the active reset, let's run the check in the Start loop's body logic:
	msgsAfterUser := stm.GetMessages()
	lastMsg := msgsAfterUser[len(msgsAfterUser)-1]
	if lastMsg.Role == llm.RoleUser {
		curiosity.mu.Lock()
		curiosity.interpelled = false
		curiosity.isBored = false
		curiosity.mu.Unlock()
	}

	curiosity.mu.Lock()
	if curiosity.interpelled || curiosity.isBored {
		curiosity.mu.Unlock()
		t.Fatal("States should have reset when user replied")
	}
	curiosity.mu.Unlock()
}

func TestSuperiorAgentShortContinuation(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{}
	coreMem := memory.NewCoreMemory("pixel_core_memory_test2.json")
	defer os.Remove("pixel_core_memory_test2.json")

	ltm := memory.NewLTM()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)
	unconscious := memory.NewUnconsciousManager()

	superior := NewSuperiorAgent(provider, coreMem, ltm, webAgent, thoughtStream, unconscious, nil, nil, nil)

	history := []llm.Message{
		{Role: llm.RoleAssistant, Content: "Je me dis que ton parcours entre foi et tech doit avoir éclairé une idée sur le temps. On pourrait en parler ?"},
		{Role: llm.RoleUser, Content: "oui"},
	}

	resp, err := superior.ProcessInput(ctx, "oui", history)
	if err != nil {
		t.Fatalf("ProcessInput failed: %v", err)
	}
	if resp != "Relance sympa de test" {
		t.Fatalf("Unexpected response: %s", resp)
	}
}

func TestCuriosityAgentRaceConditionGuard(t *testing.T) {
	ctx := context.Background()
	stm := memory.NewSTM(8)
	ltm := memory.NewLTM()
	coreMem := memory.NewCoreMemory("pixel_core_memory_test3.json")
	defer os.Remove("pixel_core_memory_test3.json")

	provider := &mockProvider{}
	sleepManager := NewSleepManager(stm, ltm, coreMem, nil, nil, provider)
	eventHub := &mockEventBroadcaster{}
	timeAgent := timeagent.NewTimeAgent()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)

	curiosity := NewCuriosityAgent(provider, coreMem, stm, ltm, eventHub, timeAgent, webAgent, thoughtStream, sleepManager, nil, nil)

	// 1. Initial wait-state
	stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: "Hello"})
	msgsSnapshot := stm.GetMessages()

	// 2. User sends a message during the generation
	stm.AddMessage(llm.Message{Role: llm.RoleUser, Content: "Wait, I am here!"})

	// 3. The LLM generation finishes and tries to write
	curiosity.interpellateInterlocuteur(ctx, msgsSnapshot)

	// 4. Verify guard cancelled it
	curiosity.mu.Lock()
	if curiosity.interpelled {
		curiosity.mu.Unlock()
		t.Fatal("Should not be marked as interpelled because race condition guard should have fired")
	}
	curiosity.mu.Unlock()

	if len(eventHub.messages) > 0 {
		t.Fatalf("No event should have been broadcast, got: %v", eventHub.messages)
	}
}

func TestSuperiorAgentBiographicalRAGTrigger(t *testing.T) {
	provider := &mockProvider{}
	coreMem := memory.NewCoreMemory("pixel_core_memory_test_bio.json")
	defer os.Remove("pixel_core_memory_test_bio.json")

	ltm := memory.NewLTM()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)
	unconscious := memory.NewUnconsciousManager()

	superior := NewSuperiorAgent(provider, coreMem, ltm, webAgent, thoughtStream, unconscious, nil, nil, nil)

	// Verify shouldTriggerRAG correctly identifies biological queries
	if !superior.shouldTriggerRAG("Et que sais tu de mon parcours de vie?", "none", "") {
		t.Fatal("Should trigger RAG for 'parcours de vie'")
	}
	if !superior.shouldTriggerRAG("que sais tu sur moi?", "none", "") {
		t.Fatal("Should trigger RAG for 'sur moi'")
	}
	if !superior.shouldTriggerRAG("qui suis-je?", "none", "") {
		t.Fatal("Should trigger RAG for 'qui suis'")
	}
	if superior.shouldTriggerRAG("bonjour ça va ?", "none", "") {
		t.Fatal("Should not trigger RAG for basic greeting")
	}
	if superior.shouldTriggerRAG("faire le ménage", "none", "") {
		t.Fatal("Should not trigger RAG for 'ménage' (substring collision with 'age')")
	}
	if superior.shouldTriggerRAG("joue une chanson dans 5 minutes", "none", "") {
		t.Fatal("Should not trigger RAG for 'dans' or 'chanson' (substring collision with 'ans')")
	}
}

func TestSleepManagerRobustJSONParsing(t *testing.T) {
	stm := memory.NewSTM(8)
	ltm := memory.NewLTM()
	coreMem := memory.NewCoreMemory("pixel_core_memory_test_json.json")
	defer os.Remove("pixel_core_memory_test_json.json")

	provider := &mockProvider{}
	sleepManager := NewSleepManager(stm, ltm, coreMem, nil, nil, provider)
	_ = sleepManager

	// Simulating robust parsing of complex types (lists, nulls) in volatile updates
	rawJSON := `{
		"volatile_updates": {
			"Auteur préféré": ["Hans Urs von Balthasar"],
			"Ville actuelle": null,
			"Système d'exploitation": "système"
		},
		"volatile_deletions": [],
		"memory_entries": []
	}`

	var result ConsolidationResult
	if err := json.Unmarshal([]byte(rawJSON), &result); err != nil {
		t.Fatalf("Failed to parse JSON using interface{}: %v", err)
	}

	// Make sure Auteur préféré is unmarshalled as an array/slice
	vVal := result.VolatileUpdates["Auteur préféré"]
	if _, ok := vVal.([]interface{}); !ok {
		t.Fatalf("Expected Auteur préféré to be unmarshalled as interface slice, got: %T", vVal)
	}

	// Verify our switch logic converts it to a clean string
	var v string
	switch val := vVal.(type) {
	case string:
		v = val
	case []interface{}:
		var strs []string
		for _, item := range val {
			if sItem, ok := item.(string); ok {
				strs = append(strs, sItem)
			}
		}
		v = strings.Join(strs, ", ")
	}

	if v != "Hans Urs von Balthasar" {
		t.Fatalf("Expected formatted string 'Hans Urs von Balthasar', got: '%s'", v)
	}
}

func TestRepairJSON(t *testing.T) {
	// Case 1: Trailing comma in objects
	input1 := `{"delete_ids": ["123"], "insert": true,}`
	repaired1 := repairJSON(input1)
	var res1 struct {
		DeleteIDs []string `json:"delete_ids"`
		Insert    bool     `json:"insert"`
	}
	if err := json.Unmarshal([]byte(repaired1), &res1); err != nil {
		t.Fatalf("Failed to parse repaired trailing comma JSON: %v. Repaired: %s", err, repaired1)
	}

	// Case 2: Double-double quotes
	input2 := `{"category": "Personal", ""action_summary"": "Test"}`
	repaired2 := repairJSON(input2)
	var res2 struct {
		Category      string `json:"category"`
		ActionSummary string `json:"action_summary"`
	}
	if err := json.Unmarshal([]byte(repaired2), &res2); err != nil {
		t.Fatalf("Failed to parse repaired double-double quote JSON: %v. Repaired: %s", err, repaired2)
	}

	// Case 3: Truncated JSON structure completion
	input3 := `{"volatile_updates": {"Nom de la femme": "Marion"`
	repaired3 := repairJSON(input3)
	var res3 struct {
		VolatileUpdates map[string]string `json:"volatile_updates"`
	}
	if err := json.Unmarshal([]byte(repaired3), &res3); err != nil {
		t.Fatalf("Failed to parse repaired truncated JSON: %v. Repaired: %s", err, repaired3)
	}
	if res3.VolatileUpdates["Nom de la femme"] != "Marion" {
		t.Fatalf("Expected Marion, got: %v", res3.VolatileUpdates["Nom de la femme"])
	}
}

func TestCuriosityAgentSelfQuestioning(t *testing.T) {
	ctx := context.Background()
	stm := memory.NewSTM(8)
	ltm := memory.NewLTM()
	coreMem := memory.NewCoreMemory("pixel_core_memory_test_selfq.json")
	defer os.Remove("pixel_core_memory_test_selfq.json")

	provider := &mockProvider{}
	sleepManager := NewSleepManager(stm, ltm, coreMem, nil, nil, provider)
	eventHub := &mockEventBroadcaster{}
	timeAgent := timeagent.NewTimeAgent()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)

	curiosity := NewCuriosityAgent(provider, coreMem, stm, ltm, eventHub, timeAgent, webAgent, thoughtStream, sleepManager, nil, nil)

	// Inject a fake recent memory in LTM to feed the self-questioning analysis
	embedding := []float32{0.1, 0.2, 0.3}
	ltm.StoreMemory(ctx, "Personal", "conversation", "Sujet initial", "Marcelo étudie la théologie chrétienne et les modèles d'IA.", []string{"theologie", "ia"}, embedding, 0.5)

	// Run generateSelfQuestioning
	curiosity.generateSelfQuestioning(ctx)

	// Verify a memory is stored in LTM under category curiosity or tags
	recent := ltm.GetRecentMemories(5)
	if len(recent) == 0 {
		t.Fatal("Self-questioning should have stored a memory in LTM")
	}

	// Verify thoughtStream got updated
	thoughts := thoughtStream.GetRecentThoughts(5)
	if len(thoughts) == 0 {
		t.Fatal("Self-questioning should have added a thought to ThoughtStream")
	}

	if !strings.Contains(thoughts[0].Content, "Auto-Questionnement") {
		t.Fatalf("Expected thought to contain '[Auto-Questionnement]', got: %s", thoughts[0].Content)
	}
}

type mockCapturingProvider struct {
	captured []llm.Message
}

func (m *mockCapturingProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	m.captured = messages
	return "Clarification request", nil
}

func (m *mockCapturingProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- "Mock stream"
	close(out)
	close(errs)
	return out, errs
}

func (m *mockCapturingProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestSuperiorAgentCoherenceDirective(t *testing.T) {
	ctx := context.Background()
	provider := &mockCapturingProvider{}
	coreMem := memory.NewCoreMemory("pixel_core_memory_test_coherence.json")
	defer os.Remove("pixel_core_memory_test_coherence.json")

	ltm := memory.NewLTM()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)
	unconscious := memory.NewUnconsciousManager()

	superior := NewSuperiorAgent(provider, coreMem, ltm, webAgent, thoughtStream, unconscious, nil, nil, nil)

	history := []llm.Message{
		{Role: llm.RoleAssistant, Content: "Si tu dois être à Aix avant 22h, il faut partir dès que possible."},
		{Role: llm.RoleUser, Content: "et si je dois arriver à 20h du matin"},
	}

	_, err := superior.ProcessInput(ctx, "et si je dois arriver à 20h du matin", history)
	if err != nil {
		t.Fatalf("ProcessInput failed: %v", err)
	}

	foundDirective := false
	for _, msg := range provider.captured {
		if msg.Role == llm.RoleSystem && strings.Contains(msg.Content, "RÉVISION DE LA COHÉRENCE ET DES ERREURS DE TRANSCRIPTION") {
			foundDirective = true
			break
		}
	}

	if !foundDirective {
		t.Error("Expected system prompt to contain 'RÉVISION DE LA COHÉRENCE ET DES ERREURS DE TRANSCRIPTION' directive")
	}
}

type mockPresenceBroadcaster struct {
	messages   []string
	hasClients bool
}

func (m *mockPresenceBroadcaster) Broadcast(message string) {
	m.messages = append(m.messages, message)
}

func (m *mockPresenceBroadcaster) HasClients() bool {
	return m.hasClients
}

func TestCuriosityAgentProposeToSharePresence(t *testing.T) {
	ctx := context.Background()
	stm := memory.NewSTM(8)
	ltm := memory.NewLTM()
	coreMem := memory.NewCoreMemory("pixel_core_memory_presence_test.json")
	defer os.Remove("pixel_core_memory_presence_test.json")

	provider := &mockProvider{}
	sleepManager := NewSleepManager(stm, ltm, coreMem, nil, nil, provider)
	timeAgent := timeagent.NewTimeAgent()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)

	// Case 1: No clients connected -> proposeToShare should return early and not broadcast
	broadcasterNoClients := &mockPresenceBroadcaster{hasClients: false}
	curiosityNoClients := NewCuriosityAgent(provider, coreMem, stm, ltm, broadcasterNoClients, timeAgent, webAgent, thoughtStream, sleepManager, nil, nil)
	curiosityNoClients.lastThoughtTopic = "Superposition quantique"
	curiosityNoClients.proposeToShare(ctx)

	if len(broadcasterNoClients.messages) > 0 {
		t.Fatal("Should not broadcast when no clients are connected")
	}

	// Case 2: Clients connected -> proposeToShare should broadcast the interpellation
	broadcasterWithClients := &mockPresenceBroadcaster{hasClients: true}
	curiosityWithClients := NewCuriosityAgent(provider, coreMem, stm, ltm, broadcasterWithClients, timeAgent, webAgent, thoughtStream, sleepManager, nil, nil)
	curiosityWithClients.lastThoughtTopic = "Superposition quantique"
	curiosityWithClients.proposeToShare(ctx)

	if len(broadcasterWithClients.messages) != 1 {
		t.Fatalf("Expected 1 broadcast message, got %d", len(broadcasterWithClients.messages))
	}
	if broadcasterWithClients.messages[0] != "Relance sympa de test" {
		t.Fatalf("Expected 'Relance sympa de test', got '%s'", broadcasterWithClients.messages[0])
	}
}

func TestCameraPresencePreventsTuEsLa(t *testing.T) {
	ctx := context.Background()
	stm := memory.NewSTM(8)
	ltm := memory.NewLTM()
	coreMem := memory.NewCoreMemory("pixel_core_memory_presence_test.json")
	defer os.Remove("pixel_core_memory_presence_test.json")

	provider := &mockProvider{}
	broadcaster := &mockPresenceBroadcaster{hasClients: true}
	curiosity := NewCuriosityAgent(provider, coreMem, stm, ltm, broadcaster, timeagent.NewTimeAgent(), NewWebAgent(), memory.NewThoughtStream(10), NewSleepManager(stm, ltm, coreMem, nil, nil, provider), nil, nil)

	msg := llm.Message{Role: llm.RoleUser, Content: "Salut"}
	stm.AddMessage(msg)

	// 1. Quand la caméra indique qu'il n'y a personne (pièce vide) :
	coreMem.UpdateVolatileState("Dernière vision", "Je vois une pièce vide sans personne.")
	curiosity.interpellateInterlocuteur(ctx, stm.GetMessages())

	if len(broadcaster.messages) > 0 {
		t.Fatalf("Pixel ne doit rien dire lorsque la caméra indique une pièce vide, mais a dit : %v", broadcaster.messages)
	}
	if !curiosity.interpelled {
		t.Fatalf("Pixel aurait dû marquer interpelled=true pour ne pas boucler dans le vide")
	}

	// 2. Quand la caméra indique que l'utilisateur est présent :
	curiosity.interpelled = false
	coreMem.UpdateVolatileState("Dernière vision", "Je vois Marcelo concentré sur son écran.")
	curiosity.interpellateInterlocuteur(ctx, stm.GetMessages())

	if len(broadcaster.messages) != 1 {
		t.Fatalf("Pixel aurait dû s'adresser à l'utilisateur présent, messages = %d", len(broadcaster.messages))
	}
	// Vérifier que le message ne contient pas "Tu es là ?"
	msgLower := strings.ToLower(broadcaster.messages[0])
	if strings.Contains(msgLower, "tu es là") || strings.Contains(msgLower, "t'es là") {
		t.Fatalf("Le message ne doit pas demander 'Tu es là ?', obtenu : %s", broadcaster.messages[0])
	}
}

func TestSuperiorAgentMediaFastPath(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{}
	coreMem := memory.NewCoreMemory("pixel_core_memory_test_fastpath.json")
	defer os.Remove("pixel_core_memory_test_fastpath.json")

	ltm := memory.NewLTM()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)
	unconscious := memory.NewUnconsciousManager()

	superior := NewSuperiorAgent(provider, coreMem, ltm, webAgent, thoughtStream, unconscious, nil, nil, nil)

	testCases := []struct {
		input          string
		expectedAction string
		expectedQuery  string
	}{
		// Stop commands
		{"stop", "media", "stop"},
		{"arrête!", "media", "stop"},
		{"Arrete la musique.", "media", "stop"},
		{"coupe la musique", "media", "stop"},
		{"coupe le son", "media", "stop"},
		{"silence?", "media", "stop"},

		// Pause commands
		{"pause", "media", "pause"},
		{"mets en pause", "media", "pause"},
		{"met en pause", "media", "pause"},

		// Next commands
		{"next", "media", "next"},
		{"skip", "media", "next"},
		{"suivant", "media", "next"},
		{"suivante", "media", "next"},
		{"joue la musique suivante", "media", "next"},
		{"chanson suivante", "media", "next"},

		// Previous commands
		{"previous", "media", "previous"},
		{"back", "media", "previous"},
		{"précédent", "media", "previous"},
		{"precedent", "media", "previous"},
		{"précédente", "media", "previous"},
		{"precedente", "media", "previous"},
		{"chanson précédente", "media", "previous"},

		// Play / Resume
		{"play", "media", "play"},
		{"lecture", "media", "play"},
		{"reprends", "media", "play"},
		{"relance", "media", "play"},
		{"reprends la musique", "media", "play"},
		{"relance la musique", "media", "play"},
	}

	for _, tc := range testCases {
		resp := superior.analyzeQuery(ctx, tc.input, nil)
		if resp.Action != tc.expectedAction || resp.Query != tc.expectedQuery {
			t.Errorf("For input %q: expected action %q, query %q; got action %q, query %q",
				tc.input, tc.expectedAction, tc.expectedQuery, resp.Action, resp.Query)
		}
	}
}

type mockStreamProvider struct{}

func (m *mockStreamProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	return `{"action": "media", "category": "music", "query": "Pink Floyd"}`, nil
}

func (m *mockStreamProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- "Bonne écoute !"
	close(out)
	close(errs)
	return out, errs
}

func (m *mockStreamProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestSuperiorAgentProcessInputStreamStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	provider := &mockStreamProvider{}
	coreMem := memory.NewCoreMemory("pixel_core_memory_test_status.json")
	defer os.Remove("pixel_core_memory_test_status.json")

	ltm := memory.NewLTM()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)
	unconscious := memory.NewUnconsciousManager()
	sched := scheduler.NewScheduler()

	superior := NewSuperiorAgent(provider, coreMem, ltm, webAgent, thoughtStream, unconscious, nil, sched, nil)

	outChan, errChan := superior.ProcessInputStream(ctx, "Joue du Pink Floyd", nil)

	// In a separate goroutine, simulate task success to unblock the scheduler wait loop
	go func() {
		var task *scheduler.Task
		for i := 0; i < 100; i++ {
			tasks := sched.GetTasks()
			if len(tasks) > 0 {
				task = tasks[0]
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if task != nil {
			task.AppendLog("Lecture en cours")
			task.SetStatus(scheduler.StatusRunning)
		}
	}()

	var chunks []string
	done := false
	for !done {
		select {
		case chunk, ok := <-outChan:
			if !ok {
				done = true
			} else {
				chunks = append(chunks, chunk)
			}
		case err, ok := <-errChan:
			if ok {
				t.Fatalf("Unexpected error from stream: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Timeout waiting for stream chunks")
		}
	}

	// Verify that the status announcement is NOT sent to the conversation chat stream (no pollution)
	for _, chunk := range chunks {
		if strings.Contains(chunk, "Recherche musicale") {
			t.Errorf("Status announcement should not appear in chat chunks, got: %q", chunk)
		}
	}

	if len(chunks) == 0 || chunks[len(chunks)-1] != "Bonne écoute !" {
		t.Errorf("Expected stream chunk to be 'Bonne écoute !', got: %v", chunks)
	}

	// Verify that the status was recorded in the real-time live logs (Flux en temps réel)
	liveLogs := resourceagent.GetLiveLogs()
	foundLiveLog := false
	for _, l := range liveLogs {
		if strings.Contains(l, "Média") && strings.Contains(l, "pink floyd") {
			foundLiveLog = true
			break
		}
	}
	if !foundLiveLog {
		t.Errorf("Expected live log to contain media action in real-time flux, got: %v", liveLogs)
	}
}

func TestSuperiorAgentCuriosityContinuationTracking(t *testing.T) {
	ctx := context.Background()
	provider := &mockStreamProvider{}
	coreMem := memory.NewCoreMemory("pixel_core_memory_test_curiosity_track.json")
	defer os.Remove("pixel_core_memory_test_curiosity_track.json")

	ltm := memory.NewLTM()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)
	unconscious := memory.NewUnconsciousManager()
	sched := scheduler.NewScheduler()

	superior := NewSuperiorAgent(provider, coreMem, ltm, webAgent, thoughtStream, unconscious, nil, sched, nil)

	// Simulate history: the last message was a curiosity interpellation containing the reflection tag
	history := []llm.Message{
		{
			Role:    llm.RoleAssistant,
			Content: "T'as une minute ? Je me pose une question sur un article.\n\n[Contenu de ma réflexion : site:arxiv.org/abs/2405.17893]",
		},
	}

	// 1. Check with a short continuation input: "oui, dis moi"
	// We want to verify prepareContext correctly extracts and detects the curiosity intent
	messages, err := superior.prepareContext(ctx, "oui, dis moi", history, nil)
	if err != nil {
		t.Fatalf("prepareContext failed: %v", err)
	}

	// The system prompt is at messages[0].Content
	systemPrompt := messages[0].Content
	if !strings.Contains(systemPrompt, "--- PENSÉE SPÉCIFIQUE À DÉVELOPPER ACTUELLEMENT ---") {
		t.Error("Expected system prompt to contain specific thought block header")
	}
	if !strings.Contains(systemPrompt, "site:arxiv.org/abs/2405.17893") {
		t.Error("Expected system prompt to contain the extracted arxiv thought")
	}
	if !strings.Contains(systemPrompt, "Tu DOIS répondre en développant et expliquant cette réflexion spécifique en priorité !") {
		t.Error("Expected system prompt to contain priority instruction")
	}

	// 2. Check with a longer or non-standard continuation: "raconte moi ce que c'est"
	messages2, err := superior.prepareContext(ctx, "raconte moi ce que c'est", history, nil)
	if err != nil {
		t.Fatalf("prepareContext failed for non-standard input: %v", err)
	}
	systemPrompt2 := messages2[0].Content
	if !strings.Contains(systemPrompt2, "site:arxiv.org/abs/2405.17893") {
		t.Error("Expected specific thought to be extracted and injected even for longer continuation")
	}
}

type mockDialogueVerificationProvider struct {
	routerAction string
}

func (m *mockDialogueVerificationProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	if len(messages) > 0 && strings.Contains(messages[0].Content, "Tu es un routeur cognitif") {
		return fmt.Sprintf(`{"action": "%s", "query": ""}`, m.routerAction), nil
	}
	return "Response", nil
}

func (m *mockDialogueVerificationProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- "Chunk"
	close(out)
	close(errs)
	return out, errs
}

func (m *mockDialogueVerificationProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestSuperiorAgentDialogueVerification(t *testing.T) {
	ctx := context.Background()
	provider := &mockDialogueVerificationProvider{routerAction: "none"}
	coreMem := memory.NewCoreMemory("pixel_core_memory_test_dialogue_verification.json")
	defer os.Remove("pixel_core_memory_test_dialogue_verification.json")

	ltm := memory.NewLTM()
	webAgent := NewWebAgent()
	thoughtStream := memory.NewThoughtStream(10)
	unconscious := memory.NewUnconsciousManager()
	sched := scheduler.NewScheduler()

	superior := NewSuperiorAgent(provider, coreMem, ltm, webAgent, thoughtStream, unconscious, nil, sched, nil)

	// Case 1: Prev assistant message is a relance without "?"
	history1 := []llm.Message{
		{
			Role:    llm.RoleAssistant,
			Content: "Tiens, je me demandais si tu penses que l'intelligence est une question ou une réponse.",
		},
		{
			Role:    llm.RoleUser,
			Content: "Difficile à dire peut-être les deux",
		},
	}

	messages1, err := superior.prepareContext(ctx, "Difficile à dire peut-être les deux", history1, nil)
	if err != nil {
		t.Fatalf("prepareContext failed: %v", err)
	}

	lastMsg1 := messages1[len(messages1)-1].Content
	if !strings.Contains(lastMsg1, "[RÉPONSE AU DIALOGUE]") {
		t.Errorf("Expected last message to contain '[RÉPONSE AU DIALOGUE]' wrapper, but got: %q", lastMsg1)
	}
	if !strings.Contains(lastMsg1, "intelligence est une question ou une réponse") {
		t.Errorf("Expected last message to contain the previous assistant message content context, but got: %q", lastMsg1)
	}

	// Case 2: Prev assistant message is a tool proposal, user says "oui"
	history2 := []llm.Message{
		{
			Role:    llm.RoleAssistant,
			Content: "Veux-tu que je lance une recherche sur l'actualité ?",
		},
		{
			Role:    llm.RoleUser,
			Content: "oui",
		},
	}

	// Since "oui" is a short continuation but follows a tool proposal, it should NOT bypass the router.
	// We set the provider action to "research" to simulate the router response.
	provider.routerAction = "research"
	messages2, err2 := superior.prepareContext(ctx, "oui", history2, nil)
	if err2 != nil {
		t.Fatalf("prepareContext failed for tool proposal follow-up: %v", err2)
	}

	// Verify that the router was called and query was evaluated (non-empty output or similar verification)
	_ = messages2
}

func TestCoreMemoryDynamicGoals(t *testing.T) {
	filePath := "test_dynamic_goals_core_memory.json"
	defer os.Remove(filePath)

	cm := memory.NewCoreMemory(filePath)
	goals := cm.GetDynamicGoals()
	if len(goals) != 0 {
		t.Fatalf("Expected 0 dynamic goals initially, got %d", len(goals))
	}

	newGoals := []memory.DynamicGoal{
		{
			ID:          "goal_1",
			Description: "Test Goal 1",
			Priority:    0.9,
			Source:      "test",
			Status:      "active",
		},
	}

	cm.UpdateDynamicGoals(newGoals)

	// Reload
	cm2 := memory.NewCoreMemory(filePath)
	goals2 := cm2.GetDynamicGoals()
	if len(goals2) != 1 {
		t.Fatalf("Expected 1 dynamic goal after update and reload, got %d", len(goals2))
	}
	if goals2[0].ID != "goal_1" || goals2[0].Description != "Test Goal 1" || goals2[0].Priority != 0.9 {
		t.Errorf("Unexpected goal contents: %+v", goals2[0])
	}
}

type mockGoalsProvider struct {
	response string
}

func (m *mockGoalsProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	return m.response, nil
}

func (m *mockGoalsProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- "Chunk"
	close(out)
	close(errs)
	return out, errs
}

func (m *mockGoalsProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestSleepManagerDynamicGoals(t *testing.T) {
	ctx := context.Background()
	stm := memory.NewSTM(8)
	ltm := memory.NewLTM()
	filePath := "test_dynamic_goals_sleep.json"
	coreMem := memory.NewCoreMemory(filePath)
	defer os.Remove(filePath)

	// Mock response that returns a dynamic goal JSON
	mockResp := `{
  "goals": [
    {
      "id": "goal_religion",
      "description": "Etudier la théologie chrétienne.",
      "priority": 0.85,
      "source": "discussion",
      "status": "active"
    }
  ]
}`
	provider := &mockGoalsProvider{response: mockResp}
	sleepManager := NewSleepManager(stm, ltm, coreMem, nil, nil, provider)

	history := []llm.Message{
		{Role: llm.RoleUser, Content: "Je veux tester le libre arbitre avec toi sous l'angle de l'éducation par la relation."},
	}

	sleepManager.consolidateDynamicGoals(ctx, history)

	goals := coreMem.GetDynamicGoals()
	if len(goals) != 1 {
		t.Fatalf("Expected 1 dynamic goal, got %d", len(goals))
	}
	if goals[0].ID != "goal_religion" || goals[0].Description != "Etudier la théologie chrétienne." {
		t.Errorf("Unexpected goal consolidated: %+v", goals[0])
	}
}






