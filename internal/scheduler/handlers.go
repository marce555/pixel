package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/voice"
)

// STMWriter interface to allow adding messages to short-term memory without circular imports.
type STMWriter interface {
	AddMessage(msg llm.Message)
}

// MusicResolver interface to decouple scheduler from agent/webagent package and avoid circular imports.
type MusicResolver interface {
	ResolveMusicURL(songQuery string) (string, string, bool, error)
}

type WebSearcher interface {
	SearchWeb(query string) (string, error)
	SearchWikipedia(query string) (string, error)
}

type EventBroadcaster interface {
	Broadcast(message string)
}

// NewPlayMusicHandler returns a task handler to play music synchronously using mpv.
func NewPlayMusicHandler(resolver MusicResolver, s *Scheduler) TaskHandler {
	return func(ctx context.Context, task *Task) error {
		var targetURL string
		var isDirect bool
		var err error

		task.mu.Lock()
		cachedURL := task.ResolvedURL
		cachedIsDirect := task.IsDirect
		task.mu.Unlock()

		if cachedURL != "" {
			task.AppendLog(fmt.Sprintf("Utilisation de la URL pré-résolue : %s", cachedURL))
			targetURL = cachedURL
			isDirect = cachedIsDirect
			if task.ResolvedTitle == "" {
				task.mu.Lock()
				task.ResolvedTitle = task.Payload
				task.mu.Unlock()
			}
		} else {
			task.AppendLog(fmt.Sprintf("Recherche et résolution de la musique : '%s'...", task.Payload))
			var resolvedTitle string
			targetURL, resolvedTitle, isDirect, err = resolver.ResolveMusicURL(task.Payload)
			if err != nil {
				task.AppendLog(fmt.Sprintf("Erreur de résolution : %v", err))
				return err
			}
			if resolvedTitle == "" {
				resolvedTitle = task.Payload
			}
			task.mu.Lock()
			task.ResolvedTitle = resolvedTitle
			task.mu.Unlock()
		}

		if isDirect {
			task.AppendLog(fmt.Sprintf("Piste directe trouvée sur YouTube : %s", targetURL))
		} else {
			task.AppendLog(fmt.Sprintf("Piste directe non trouvée. Ouverture de la page de recherche : %s", targetURL))
		}

		// Build env with corrected PATH: remove existing PATH entry and prepend localBin.
		// IMPORTANT: appending PATH= doesn't work in Go exec — the first occurrence wins.
		localBin := "/home/marceloc/Documents/Pixel/bin"
		currentPath := os.Getenv("PATH")
		baseEnv := os.Environ()
		filteredEnv := make([]string, 0, len(baseEnv)+1)
		for _, e := range baseEnv {
			if !strings.HasPrefix(e, "PATH=") {
				filteredEnv = append(filteredEnv, e)
			}
		}
		filteredEnv = append(filteredEnv, "PATH="+localBin+":"+currentPath)

		ipcSocket := fmt.Sprintf("/tmp/mpvsocket_%s", task.ID)

		ytdlRawOpts := "extractor-args=[youtube:player_client=default,-tv_samsung]"
		if strings.Contains(targetURL, "list=") {
			ytdlRawOpts += ",yes-playlist="
			task.AppendLog("Détection d'une playlist. Activation du mode lecture continue.")
		}

		// Run mpv with native PipeWire audio output (CachyOS default)
		// Audio filter silenceremove trims leading silence and cuts off trailing silence
		// to allow instant punch-in at 100% volume and clean radio-style transitions.
		args := []string{
			"--no-video",
			"--ao=pipewire",
			"--ytdl-format=bestaudio/best",
			"--af=lavfi=[silenceremove=start_periods=1:start_duration=0.02:start_threshold=-40dB:stop_periods=-1:stop_duration=0.8:stop_threshold=-40dB]",
			fmt.Sprintf("--ytdl-raw-options=%s", ytdlRawOpts),
			fmt.Sprintf("--input-ipc-server=%s", ipcSocket), // IPC socket for play/pause/stop control
		}
		mpvTarget := targetURL
		if !isDirect && strings.Contains(targetURL, "youtube.com/results?search_query=") {
			cleanPayload := task.Payload
			qLower := strings.ToLower(cleanPayload)
			if !strings.Contains(qLower, "lyrics") && !strings.Contains(qLower, "paroles") && !strings.Contains(qLower, "live") && !strings.Contains(qLower, "playlist") {
				cleanPayload += " lyrics"
			}
			mpvTarget = fmt.Sprintf("ytdl://ytsearch1:%s", cleanPayload)
		}
		args = append(args, mpvTarget)

		var speaker VoiceSpeaker
		if s != nil {
			speaker = s.GetVoiceSpeaker()
		}

		// Opening announcement for the very first track if not yet announced
		if speaker != nil && task.GetAnnouncement() != "" && !task.IsAnnounced() {
			task.SetAnnounced(true)
			task.AppendLog(fmt.Sprintf("Pixel annonce le premier titre : '%s'...", task.GetAnnouncement()))
			_ = speaker.SpeakText(ctx, task.GetAnnouncement())
		}

		cmd := exec.CommandContext(ctx, "mpv", args...)
		cmd.Env = filteredEnv

		task.SetCmd(cmd)

		task.AppendLog("Lancement du lecteur audio mpv...")
		fmt.Printf("[Scheduler/mpv] Démarrage : mpv %s\n", strings.Join(args, " "))
		if err := cmd.Start(); err != nil {
			task.AppendLog(fmt.Sprintf("Erreur de démarrage de mpv : %v", err))
			fmt.Printf("[Scheduler/mpv] ERREUR de démarrage : %v\n", err)
			return fmt.Errorf("impossible de démarrer mpv : %w", err)
		}
		fmt.Printf("[Scheduler/mpv] PID mpv : %d\n", cmd.Process.Pid)

		// Start background pre-resolving for the next task in queue
		if s != nil {
			go func() {
				nextTask := s.GetNextPendingMusicTask(task.ID)

				// Autoplay (Infinite Radio) logic
				if nextTask == nil && s.AutoQueueCallback != nil && task.ResolvedTitle != "" {
					task.AppendLog("File d'attente vide. Appel de l'Autoplay (Radio Infinie)...")
					s.TriggerAutoplay(task.ResolvedTitle)

					// Wait for the LLM to generate and enqueue the next song (poll for up to 60s)
					for i := 0; i < 30; i++ {
						time.Sleep(2 * time.Second)
						nextTask = s.GetNextPendingMusicTask(task.ID)
						if nextTask != nil {
							break
						}
					}
				}

				if nextTask != nil {
					nextTask.AppendLog("Pré-résolution de la musique en arrière-plan...")
					u, title, dir, err := resolver.ResolveMusicURL(nextTask.Payload)
					if err == nil {
						nextTask.mu.Lock()
						nextTask.ResolvedURL = u
						nextTask.ResolvedTitle = title
						nextTask.IsDirect = dir
						nextTask.mu.Unlock()
						nextTask.AppendLog(fmt.Sprintf("Pré-résolution réussie (streaming direct) : %s (%s)", u, title))
					} else {
						nextTask.AppendLog(fmt.Sprintf("Échec de la pré-résolution : %v", err))
					}

					// Pre-synthesize the voice announcement for nextTask if present
					nextAnn := nextTask.GetAnnouncement()
					if nextAnn != "" && speaker != nil && nextTask.GetAnnouncementWav() == "" {
						annWav := filepath.Join(os.TempDir(), fmt.Sprintf("pixel_ann_%s.wav", nextTask.ID))
						if errWav := speaker.SynthesizeToFile(ctx, nextAnn, annWav); errWav == nil {
							nextTask.SetAnnouncementWav(annWav)
							nextTask.AppendLog(fmt.Sprintf("Annonce vocale pré-synthétisée : '%s'", nextAnn))
						}
					}
				}
			}()
		}

		speechDone := make(chan struct{})
		var hasSpeechStarted bool

		// Background transition monitor with Audio Ducking & Radio DJ voiceover
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			released := false
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					conn, err := net.Dial("unix", ipcSocket)
					if err != nil {
						continue
					}
					fmt.Fprintf(conn, `{"command": ["get_property", "time-remaining"]}`+"\n")

					var response struct {
						Data  float64 `json:"data"`
						Error string  `json:"error"`
					}
					decoder := json.NewDecoder(conn)
					for {
						if err := decoder.Decode(&response); err != nil {
							break
						}
						if response.Error == "success" {
							var nextTask *Task
							if s != nil {
								nextTask = s.GetNextPendingMusicTask(task.ID)
							}

							hasAnnouncement := nextTask != nil && nextTask.GetAnnouncement() != "" && !nextTask.IsAnnounced()
							threshold := 2.5
							if hasAnnouncement {
								threshold = 5.5
							}

							if response.Data <= threshold && !released {
								released = true

								if hasAnnouncement && speaker != nil {
									hasSpeechStarted = true
									nextTask.SetAnnounced(true)
									task.AppendLog(fmt.Sprintf("Radio DJ : Ducking du volume à 20%% et annonce vocale : '%s'...", nextTask.GetAnnouncement()))

									// 1. Duck volume to 20%
									duckConn, errDuck := net.Dial("unix", ipcSocket)
									if errDuck == nil {
										fmt.Fprintf(duckConn, `{"command": ["set_property", "volume", 20.0]}`+"\n")
										duckConn.Close()
									}

									// 2. Play speech over ducked music
									go func() {
										annWav := nextTask.GetAnnouncementWav()
										if annWav != "" {
											if _, statErr := os.Stat(annWav); statErr == nil {
												_ = voice.PlayAudioFile(ctx, annWav)
												_ = os.Remove(annWav)
											} else {
												_ = speaker.SpeakText(ctx, nextTask.GetAnnouncement())
											}
										} else {
											_ = speaker.SpeakText(ctx, nextTask.GetAnnouncement())
										}

										// 3. Outgoing track fade-out to 0
										fadeConn, errFade := net.Dial("unix", ipcSocket)
										if errFade == nil {
											fmt.Fprintf(fadeConn, `{"command": ["set_property", "volume", 0.0]}`+"\n")
											fadeConn.Close()
										}

										// 4. Release next track to attack directly at 100%
										if task.ReleaseSem != nil {
											task.ReleaseSem()
										}
										close(speechDone)
									}()
								} else {
									if task.ReleaseSem != nil {
										task.ReleaseSem()
									}

									task.AppendLog("Enchaînement direct : lancement du titre suivant à 100% et descente du volume...")

									// Start Fade-Out Goroutine (quick smooth descent over ~2 seconds)
									go func() {
										fadeTicker := time.NewTicker(50 * time.Millisecond)
										defer fadeTicker.Stop()
										vol := 100.0
										for {
											select {
											case <-ctx.Done():
												return
											case <-fadeTicker.C:
												vol -= 2.5 // 100 to 0 over 40 steps (2 seconds)
												if vol < 0 {
													vol = 0
												}
												fadeConn, errDial := net.Dial("unix", ipcSocket)
												if errDial == nil {
													fmt.Fprintf(fadeConn, `{"command": ["set_property", "volume", %f]}`+"\n", vol)
													fadeConn.Close()
												}
												if vol <= 0 {
													return
												}
											}
										}
									}()
								}
							}
							break
						}
					}
					conn.Close()
				}
			}
		}()

		task.AppendLog("Lecture en cours. En attente de la fin du morceau...")
		waitErr := cmd.Wait()

		// If speech announcement is running, wait for it before exiting
		if hasSpeechStarted {
			select {
			case <-speechDone:
			case <-time.After(8 * time.Second):
			}
		}

		// Check if task was cancelled during execution
		if ctx.Err() != nil {
			task.AppendLog("La lecture a été interrompue (tâche annulée ou passée).")
			return ctx.Err()
		}

		if waitErr != nil {
			task.AppendLog(fmt.Sprintf("mpv s'est arrêté avec une erreur : %v", waitErr))
			return fmt.Errorf("erreur durant la lecture mpv : %w", waitErr)
		}

		task.AppendLog("Chanson terminée.")
		return nil
	}
}
