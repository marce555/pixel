package voice

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type VoiceManager struct {
	binDir           string
	piperPath        string
	modelPath        string
	modelUrl         string
	configUrl        string
	piperUrl         string
	asrEndpoint      string
	whisperServerCmd *exec.Cmd
	serverMu         sync.Mutex
}

func NewVoiceManager() (*VoiceManager, error) {
	baseDir, err := os.Getwd()
	if err != nil {
		baseDir = "."
	}
	binDir := filepath.Join(baseDir, "bin")
	piperDir := filepath.Join(binDir, "piper")

	vm := &VoiceManager{
		binDir:      binDir,
		piperPath:   filepath.Join(piperDir, "piper"),
		modelPath:   filepath.Join(binDir, "fr_FR-siwis-medium.onnx"),
		modelUrl:    "https://huggingface.co/rhasspy/piper-voices/resolve/v1.0.0/fr/fr_FR/siwis/medium/fr_FR-siwis-medium.onnx",
		configUrl:   "https://huggingface.co/rhasspy/piper-voices/resolve/v1.0.0/fr/fr_FR/siwis/medium/fr_FR-siwis-medium.onnx.json",
		piperUrl:    "https://github.com/rhasspy/piper/releases/download/v1.2.0/piper_amd64.tar.gz",
		asrEndpoint: "http://127.0.0.1:8089/inference",
	}

	// S'assurer de la présence des binaires et lancer whisper-server en tâche de fond
	go func() {
		err := vm.ensureAssets()
		if err != nil {
			fmt.Printf("[VoiceManager] Erreur lors de l'initialisation des fichiers vocaux : %v\n", err)
		} else {
			fmt.Println("[VoiceManager] Moteur Piper et modèle de voix prêts.")
		}
		// Démarrer whisper-server persistant GPU si disponible
		vm.EnsureWhisperServer(context.Background())
	}()

	return vm, nil
}

// ConvertWebMToWav décode le fichier WebM audio du navigateur en WAV 16kHz mono PCM compatible Whisper.
func (vm *VoiceManager) ConvertWebMToWav(webmPath, wavPath string) error {
	// Commande ffmpeg pour transcoder avec traitement DSP voix haut de gamme :
	// - aformat=channel_layouts=mono : downmix natif stéréo→mono (inclut les deux canaux, évite le canal gauche vide
	//   sur les drivers PipeWire/PulseAudio Linux qui placent le signal sur les deux canaux en WebM)
	// - highpass=f=80 : coupe les bruits sourds de basse fréquence et les vibrations
	// - lowpass=f=10000 : conserve les sibilantes françaises (s, f, ch) qui montent jusqu'à 10kHz
	// - equalizer f=3000 : boost de 4dB sur les fréquences de parole pour micro laptop à signal faible
	// - volume=3.0 : gain renforcé pour les micros intégrés à faible sensibilité
	cmd := exec.Command("ffmpeg", "-y", "-i", webmPath, "-ar", "16000", "-af",
		"aformat=channel_layouts=mono,highpass=f=80,lowpass=f=8000,volume=1.2",
		"-c:a", "pcm_s16le", wavPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("ffmpeg failed: %w (stderr: %s)", err, stderr.String())
	}

	// Diagnostic : loguer la taille du WAV produit pour détecter les fichiers vides
	if info, statErr := os.Stat(wavPath); statErr == nil {
		fmt.Printf("[VoiceManager] WAV produit : %d octets (%.1f ms audio estimé)\n", info.Size(), float64(info.Size()-44)/32.0)
	}

	return nil
}

// EnsureWhisperServer s'assure que le serveur GPU whisper-server tourne sur le port 8089.
func (vm *VoiceManager) EnsureWhisperServer(ctx context.Context) error {
	vm.serverMu.Lock()
	defer vm.serverMu.Unlock()

	// Vérifier si le serveur répond déjà
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:8089/inference")
	if err == nil {
		resp.Body.Close()
		return nil // Déjà actif !
	}

	serverPath := filepath.Join(vm.binDir, "whisper.cpp", "build", "bin", "whisper-server")
	if _, err := os.Stat(serverPath); os.IsNotExist(err) {
		return fmt.Errorf("whisper-server introuvable à %s", serverPath)
	}

	modelsDir := filepath.Join(vm.binDir, "whisper.cpp", "models")
	modelPath := filepath.Join(modelsDir, "ggml-medium-q5_0.bin")
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		modelPath = filepath.Join(modelsDir, "ggml-base.bin")
	}

	vadModelPath := filepath.Join(modelsDir, "for-tests-silero-v6.2.0-ggml.bin")

	args := []string{
		"-m", modelPath,
		"-l", "fr",
		"-t", "4",
		"--suppress-nst",
		"--beam-size", "5",
		"--best-of", "5",
		"--port", "8089",
	}

	if _, err := os.Stat(vadModelPath); err == nil {
		args = append(args, "--vad", "--vad-model", vadModelPath, "--vad-threshold", "0.5")
	}

	cmd := exec.Command(serverPath, args...)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/opt/cuda/lib64:"+os.Getenv("LD_LIBRARY_PATH"))

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("impossible de démarrer whisper-server : %w", err)
	}

	vm.whisperServerCmd = cmd
	fmt.Println("[VoiceManager] Serveur GPU persistant Whisper lancé sur http://127.0.0.1:8089")

	time.Sleep(1 * time.Second)
	return nil
}

func (vm *VoiceManager) transcribeViaServer(ctx context.Context, wavPath string) (string, error) {
	fileData, err := os.ReadFile(wavPath)
	if err != nil {
		return "", err
	}

	var reqBody bytes.Buffer
	writer := multipart.NewWriter(&reqBody)
	part, err := writer.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		return "", err
	}
	_, err = part.Write(fileData)
	if err != nil {
		return "", err
	}
	writer.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", vm.asrEndpoint, &reqBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("whisper-server error %d: %s", resp.StatusCode, string(respBytes))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.Text, nil
}

func (vm *VoiceManager) cleanWhisperText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "[BLANK_AUDIO]", "")
	lines := strings.Split(text, "\n")
	var cleanLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && trimmed != "..." && trimmed != ".." && trimmed != "." {
			cleanLines = append(cleanLines, trimmed)
		}
	}
	text = strings.TrimSpace(strings.Join(cleanLines, " "))

	hallucinationPatterns := []string{
		"amara.org",
		"sous-titres réalisés",
		"sous-titres par la communauté",
		"subtitles by the amara",
		"transcribed by",
		"merci d'avoir regardé",
		"merci de regarder",
		"abonnez-vous",
		"n'oubliez pas de liker",
		"conversation en français avec pixel",
		"une assistante ia",
	}
	textLower := strings.ToLower(text)
	for _, pattern := range hallucinationPatterns {
		if strings.Contains(textLower, pattern) {
			fmt.Printf("[VoiceManager] Hallucination Whisper détectée et ignorée: '%s'\n", text)
			return ""
		}
	}
	return text
}

// TranscribeAudio exécute la transcription via whisper-server GPU ou fallback whisper-cli.
func (vm *VoiceManager) TranscribeAudio(ctx context.Context, wavPath string) (string, error) {
	// 1. Essayer le serveur GPU persistant whisper-server (sub-50ms)
	text, err := vm.transcribeViaServer(ctx, wavPath)
	if err == nil {
		cleaned := vm.cleanWhisperText(text)
		return cleaned, nil
	}

	// Tenter de démarrer whisper-server si pas encore prêt
	go vm.EnsureWhisperServer(context.Background())

	// 2. Fallback sur whisper-cli
	modelsDir := filepath.Join(vm.binDir, "whisper.cpp", "models")
	type modelCandidate struct {
		path  string
		label string
	}
	candidates := []modelCandidate{
		{filepath.Join(modelsDir, "ggml-medium-q5_0.bin"), "medium-q5_0 (GPU)"},
		{filepath.Join(modelsDir, "ggml-small-q5_1.bin"), "small-q5_1 (CPU)"},
		{filepath.Join(modelsDir, "ggml-base.bin"), "base (fallback CPU)"},
	}

	modelPath := ""
	modelLabel := ""
	for _, c := range candidates {
		if _, err := os.Stat(c.path); err == nil {
			modelPath = c.path
			modelLabel = c.label
			break
		}
	}
	if modelPath == "" {
		return "", fmt.Errorf("aucun modèle Whisper disponible, veuillez patienter...")
	}

	cliPath := filepath.Join(vm.binDir, "whisper.cpp", "build", "bin", "whisper-cli")
	if _, err := os.Stat(cliPath); os.IsNotExist(err) {
		return "", fmt.Errorf("le décodeur vocal local whisper-cli est en cours de compilation...")
	}

	vadModelPath := filepath.Join(modelsDir, "for-tests-silero-v6.2.0-ggml.bin")
	tempOutput := filepath.Join(os.TempDir(), fmt.Sprintf("whisper_out_%d", time.Now().UnixNano()))
	defer os.Remove(tempOutput + ".txt")

	args := []string{
		"-t", "4",
		"-m", modelPath,
		"-f", wavPath,
		"-otxt",
		"-of", tempOutput,
		"-l", "fr",
		"--suppress-nst",
		"--no-speech-thold", "0.5",
		"--entropy-thold", "2.8",
		"--temperature", "0.0",
		"--beam-size", "5",
		"--word-thold", "0.01",
		"--max-len", "0",
	}

	if modelLabel == "medium-q5_0 (GPU)" {
		args = append(args, "--device", "0", "--flash-attn")
	}

	if _, err := os.Stat(vadModelPath); err == nil {
		args = append(args,
			"--vad-model", vadModelPath,
			"--vad-threshold", "0.5",
			"--vad-min-speech-duration-ms", "250",
			"--vad-speech-pad-ms", "100",
		)
	}

	cmd := exec.CommandContext(ctx, cliPath, args...)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/opt/cuda/lib64:"+os.Getenv("LD_LIBRARY_PATH"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr != nil {
		return "", fmt.Errorf("whisper-cli execution failed: %w (stderr: %s)", runErr, stderr.String())
	}

	textBytes, readErr := os.ReadFile(tempOutput + ".txt")
	if readErr != nil {
		return "", fmt.Errorf("impossible de lire le fichier de transcription : %w", readErr)
	}

	return vm.cleanWhisperText(string(textBytes)), nil
}

// Synthesize utilise Piper localement sur le CPU pour générer le WAV audio correspondant au texte
func (vm *VoiceManager) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if !vm.isPiperReady() {
		return nil, fmt.Errorf("le moteur de synthèse vocale Piper est en cours de téléchargement, veuillez patienter")
	}

	// Nettoyer un peu le texte (supprimer retours à la ligne intempestifs)
	text = strings.ReplaceAll(text, "\n", " ")

	tempWav := filepath.Join(os.TempDir(), fmt.Sprintf("piper_%d.wav", time.Now().UnixNano()))
	defer os.Remove(tempWav)

	// Lancer le binaire piper en lui passant le texte via l'entrée standard (stdin)
	// La voix Siwis étant mono-locuteur, le paramètre --speaker n'est pas nécessaire.
	cmd := exec.CommandContext(ctx, vm.piperPath, "--model", vm.modelPath, "--output_file", tempWav)
	
	// Spécifier LD_LIBRARY_PATH pour que Piper charge ses bibliothèques partagées ONNX Runtime
	piperDir := filepath.Dir(vm.piperPath)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+piperDir)
	
	cmd.Stdin = strings.NewReader(text)
	
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("piper execution failed: %w (stderr: %s)", err, stderr.String())
	}

	// Lire le fichier WAV produit
	audioBytes, err := os.ReadFile(tempWav)
	if err != nil {
		return nil, fmt.Errorf("impossible de lire le fichier WAV généré par Piper : %w", err)
	}

	return audioBytes, nil
}

func (vm *VoiceManager) isPiperReady() bool {
	_, err1 := os.Stat(vm.piperPath)
	_, err2 := os.Stat(vm.modelPath)
	return err1 == nil && err2 == nil
}

// ensureAssets gère le téléchargement automatique et l'extraction de Piper + modèle de voix
func (vm *VoiceManager) ensureAssets() error {
	err := os.MkdirAll(vm.binDir, 0755)
	if err != nil {
		return err
	}

	// 1. Télécharger le modèle de voix s'il est manquant
	if _, err := os.Stat(vm.modelPath); os.IsNotExist(err) {
		fmt.Println("[VoiceManager] Téléchargement du modèle de voix (medium)...")
		err = vm.downloadFile(vm.modelUrl, vm.modelPath)
		if err != nil {
			return fmt.Errorf("téléchargement du modèle ONNX échoué : %w", err)
		}
	}

	// Télécharger la configuration de la voix s'il elle est manquante
	configPath := vm.modelPath + ".json"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		err = vm.downloadFile(vm.configUrl, configPath)
		if err != nil {
			return fmt.Errorf("téléchargement de la config ONNX.json échoué : %w", err)
		}
	}

	// 2. Télécharger et extraire le binaire Piper s'il est manquant
	if _, err := os.Stat(vm.piperPath); os.IsNotExist(err) {
		fmt.Println("[VoiceManager] Téléchargement du moteur neuronal Piper (tar.gz)...")
		tarGzPath := filepath.Join(vm.binDir, "piper.tar.gz")
		defer os.Remove(tarGzPath)

		err = vm.downloadFile(vm.piperUrl, tarGzPath)
		if err != nil {
			return fmt.Errorf("téléchargement du binaire Piper échoué : %w", err)
		}

		fmt.Println("[VoiceManager] Extraction du moteur Piper...")
		err = vm.extractTarGz(tarGzPath, vm.binDir)
		if err != nil {
			return fmt.Errorf("extraction du moteur Piper échouée : %w", err)
		}

		// S'assurer de la permission d'exécution sur le binaire
		err = os.Chmod(vm.piperPath, 0755)
		if err != nil {
			return fmt.Errorf("chmod +x sur le binaire Piper échoué : %w", err)
		}
	}

	// S'assurer de la présence du lien symbolique libespeak-ng.so.1 indispensable pour charger Piper
	libSymlink := filepath.Join(vm.binDir, "piper", "libespeak-ng.so.1")
	if _, err := os.Lstat(libSymlink); os.IsNotExist(err) {
		fmt.Println("[VoiceManager] Création du lien symbolique indispensable libespeak-ng.so.1...")
		_ = os.Symlink("libespeak-ng.so.1.1.51", libSymlink)
	}

	return nil
}

func (vm *VoiceManager) downloadFile(url string, destPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("le serveur de téléchargement a retourné le statut %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func (vm *VoiceManager) extractTarGz(tarGzPath, destDir string) error {
	file, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Extraire les fichiers du dossier "piper/" interne à l'archive
		// L'archive contient un dossier racine "piper/"
		parts := strings.Split(header.Name, "/")
		if len(parts) <= 1 {
			continue // Ignorer le dossier racine lui-même
		}
		
		// Recréer le chemin cible
		relPath := strings.Join(parts[1:], "/")
		targetPath := filepath.Join(destDir, "piper", relPath)

		switch header.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(targetPath, 0755)
			if err != nil {
				return err
			}
		case tar.TypeReg:
			err = os.MkdirAll(filepath.Dir(targetPath), 0755)
			if err != nil {
				return err
			}

			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			
			_, err = io.Copy(outFile, tarReader)
			outFile.Close()
			if err != nil {
				return err
			}
		}
	}

	return nil
}
