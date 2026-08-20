package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/marce555/pixel/internal/llm"
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
		
		// Run mpv with the cancelable task context
		args := []string{
			"--no-video",
			fmt.Sprintf("--input-ipc-server=%s", ipcSocket), // IPC socket for play/pause/stop control
		}
		if strings.Contains(targetURL, "list=") {
			args = append(args, "--ytdl-raw-options=yes-playlist=")
			task.AppendLog("Détection d'une playlist. Activation du mode lecture continue.")
		}
		args = append(args, targetURL)

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
						finalURL := u
						if dir && !strings.Contains(u, "list=") && title != "" {
							os.MkdirAll("/tmp/pixel_cache", 0755)
							safeTitle := strings.Map(func(r rune) rune {
								if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
									return r
								}
								return '_'
							}, title)
							cachePath := fmt.Sprintf("/tmp/pixel_cache/%s.mp3", safeTitle)

							if _, statErr := os.Stat(cachePath); os.IsNotExist(statErr) {
								nextTask.AppendLog("Téléchargement en arrière-plan pour le cache...")
								dlCmd := exec.Command("yt-dlp", "-x", "--audio-format", "mp3", "-o", cachePath, u)
								localBin := "/home/marceloc/Documents/Pixel/bin"
								dlCmd.Env = append(os.Environ(), "PATH="+localBin+":"+os.Getenv("PATH"))
								if dlErr := dlCmd.Run(); dlErr == nil {
									finalURL = cachePath
									nextTask.AppendLog("Mise en cache terminée avec succès.")
								} else {
									nextTask.AppendLog(fmt.Sprintf("Échec de la mise en cache : %v", dlErr))
								}
							} else {
								finalURL = cachePath
								nextTask.AppendLog("Piste trouvée dans le cache local.")
							}
						}

						nextTask.mu.Lock()
						nextTask.ResolvedURL = finalURL
						nextTask.ResolvedTitle = title
						nextTask.IsDirect = dir
						nextTask.mu.Unlock()
						nextTask.AppendLog(fmt.Sprintf("Pré-résolution réussie : %s (%s)", finalURL, title))
					} else {
						nextTask.AppendLog(fmt.Sprintf("Échec de la pré-résolution : %v", err))
					}
				}
			}()
		}

		// Background crossfade monitor
		go func() {
			ticker := time.NewTicker(1 * time.Second)
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
							if response.Data <= 5.0 && !released {
								released = true
								if task.ReleaseSem != nil {
									task.ReleaseSem()
								}
								
								task.AppendLog("Mixage en cours (Fade-out progressif sur 5s)...")
								
								// Start Fade-Out Goroutine
								go func() {
									fadeTicker := time.NewTicker(100 * time.Millisecond)
									defer fadeTicker.Stop()
									vol := 100.0
									for {
										select {
										case <-ctx.Done():
											return
										case <-fadeTicker.C:
											vol -= 2.0 // 100 to 0 over 50 steps (5 seconds)
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
							break
						}
					}
					conn.Close()
				}
			}
		}()

		task.AppendLog("Lecture en cours. En attente de la fin du morceau...")
		waitErr := cmd.Wait()

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
