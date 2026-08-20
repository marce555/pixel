package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteSkill(t *testing.T) {
	// Create a temporary skills directory
	tempDir := t.TempDir()
	
	// Create a dummy skill
	skillName := "test_skill"
	skillDir := filepath.Join(tempDir, skillName)
	os.MkdirAll(skillDir, 0755)
	
	// Write run.py
	pythonCode := `import sys
print("Hello from test skill! Query:", sys.argv[1])`
	os.WriteFile(filepath.Join(skillDir, "run.py"), []byte(pythonCode), 0755)
	
	// Write manifest.json
	manifestJSON := `{"name": "test_skill", "description": "A test skill", "router_instructions": "Utilise ceci."}`
	os.WriteFile(filepath.Join(skillDir, "manifest.json"), []byte(manifestJSON), 0644)
	
	// Initialize SkillManager
	sm := NewSkillManager(nil, tempDir)
	
	// Test execution
	result, err := sm.ExecuteSkill(context.Background(), "test_skill", "World")
	if err != nil {
		t.Fatalf("ExecuteSkill failed: %v", err)
	}
	
	expected := "Hello from test skill! Query: World"
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}

	// Test GetSkillsDescriptionForSystemPrompt
	sysPromptDesc := sm.GetSkillsDescriptionForSystemPrompt()
	if sysPromptDesc == "" {
		t.Errorf("Expected non-empty system prompt skills description")
	}
}

