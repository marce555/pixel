package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marce555/pixel/internal/memory"
)

func TestVisionAgentSkipsCaptureWhenCameraDisabled(t *testing.T) {
	coreMem := memory.NewCoreMemory(t.TempDir() + "/core.json")
	coreMem.SetCameraEnabled(false)

	var broadcasts []string
	va := NewVisionAgent(&mockIntroduceProvider{}, coreMem, memory.NewLTM(), memory.NewSTM(8), nil, func(msg string) {
		broadcasts = append(broadcasts, msg)
	})

	ctx := context.Background()
	if desc, err := va.CaptureAndAnalyze(ctx); !errors.Is(err, ErrCameraDisabled) || desc != "" {
		t.Fatalf("expected CaptureAndAnalyze ErrCameraDisabled, got desc=%q err=%v", desc, err)
	}
	if desc, err := va.ScanOnce(ctx); !errors.Is(err, ErrCameraDisabled) || desc != "" {
		t.Fatalf("expected ScanOnce ErrCameraDisabled, got desc=%q err=%v", desc, err)
	}
	if desc, err := va.ScanOnceOnOpening(ctx); !errors.Is(err, ErrCameraDisabled) || desc != "" {
		t.Fatalf("expected ScanOnceOnOpening ErrCameraDisabled, got desc=%q err=%v", desc, err)
	}
	if len(broadcasts) == 0 || !strings.Contains(strings.ToLower(broadcasts[0]), "caméra éteinte") {
		t.Fatalf("expected VISION camera-off broadcast, got %v", broadcasts)
	}
}

func TestCuriosityAgentSkipsCameraWhenDisabled(t *testing.T) {
	coreMem := memory.NewCoreMemory(t.TempDir() + "/core.json")
	coreMem.SetCameraEnabled(false)

	va := NewVisionAgent(&mockIntroduceProvider{}, coreMem, memory.NewLTM(), memory.NewSTM(8), nil, func(string) {})
	ca := &CuriosityAgent{
		coreMemory:  coreMem,
		visionAgent: va,
	}

	if ca.checkPresenceWithCamera(context.Background()) {
		t.Fatalf("expected checkPresenceWithCamera to return false when camera is disabled")
	}
	gotVision := coreMem.GetProfile().Volatile["Dernière vision"]
	if !strings.Contains(strings.ToLower(gotVision), "caméra éteinte") {
		t.Fatalf("expected Dernière vision to stay camera-off notice, got %q", gotVision)
	}
}

func TestSuperiorAgentKnowsCameraDisabled(t *testing.T) {
	coreMem := memory.NewCoreMemory(t.TempDir() + "/core.json")
	coreMem.SetCameraEnabled(false)

	provider := &mockIntroduceProvider{generateResp: "Ma caméra est éteinte."}
	ltm := memory.NewLTM()
	stm := memory.NewSTM(8)
	sa := NewSuperiorAgent(provider, coreMem, ltm, nil, nil, nil, nil, nil, nil)
	va := NewVisionAgent(provider, coreMem, ltm, stm, nil, func(string) {})
	sa.SetVisionAgent(va)

	sysPrompt := sa.buildSystemPrompt(nil, "")
	if !strings.Contains(sysPrompt, "CAMÉRA ÉTEINTE / DÉSACTIVÉE") {
		t.Fatalf("expected system prompt to include CAMÉRA ÉTEINTE / DÉSACTIVÉE directive")
	}

	ctx := context.Background()
	route := sa.analyzeQuery(ctx, "tu vois quoi devant toi ?", nil)
	if route.Action == "skill_decouvrir_nouveau_visage" {
		t.Fatalf("expected analyzeQuery not to route to skill_decouvrir_nouveau_visage when camera is disabled")
	}

	resp, err := sa.ProcessInput(ctx, "regarde qui est là à la caméra", nil)
	if err != nil {
		t.Fatalf("ProcessInput failed: %v", err)
	}
	if strings.Contains(resp, "je te regarde à la caméra") || strings.Contains(resp, "j'active la caméra") {
		t.Fatalf("expected ProcessInput to skip camera activation prefix when camera is disabled, got %q", resp)
	}

	introResp, err := sa.handleIntroduction(ctx, "Alice")
	if err != nil {
		t.Fatalf("handleIntroduction failed: %v", err)
	}
	if strings.Contains(introResp, "je te regarde avec ma caméra") {
		t.Fatalf("expected handleIntroduction not to claim using camera when disabled, got %q", introResp)
	}
}
