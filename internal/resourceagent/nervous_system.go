package resourceagent

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SystemStatus struct {
	InternetActive bool    `json:"internet_active"`
	RAMUsedPercent float64 `json:"ram_used_percent"`
	LoadAverage1   float64 `json:"load_average_1"`
	NumCPUs        int     `json:"num_cpus"`
	HealthStatus   string  `json:"health_status"`
	Logs           string  `json:"logs"`
}

var (
	internetCacheMu      sync.Mutex
	lastInternetCheck    time.Time
	cachedInternetActive bool

	liveLogsMu sync.Mutex
	liveLogs   []string
)

type liveLogWriter struct {
	out io.Writer
}

func (w *liveLogWriter) Write(p []byte) (n int, err error) {
	n, err = w.out.Write(p)
	raw := string(p)
	msg := strings.TrimSpace(raw)
	if len(msg) > 0 {
		tag := "Système"
		cleanMsg := msg
		if idx := strings.Index(msg, "] "); idx != -1 && strings.Contains(msg[:idx], "[") {
			tagIdx := strings.LastIndex(msg[:idx+1], "[")
			tag = msg[tagIdx+1 : idx]
			cleanMsg = strings.TrimSpace(msg[idx+2:])
		}
		recordLiveLog(tag, cleanMsg)
	}
	return n, err
}

func init() {
	log.SetOutput(&liveLogWriter{out: os.Stderr})
}

func recordLiveLog(tag, cleanMsg string) {
	cleanMsg = strings.ReplaceAll(cleanMsg, "\n", " ")
	liveLogsMu.Lock()
	defer liveLogsMu.Unlock()
	timestamp := time.Now().Format("15:04:05")
	formatted := fmt.Sprintf("[%s] [%s] %s", timestamp, tag, cleanMsg)
	
	// Éviter les doublons consécutifs identiques
	if len(liveLogs) > 0 && liveLogs[len(liveLogs)-1] == formatted {
		return
	}

	liveLogs = append(liveLogs, formatted)
	if len(liveLogs) > 30 {
		liveLogs = liveLogs[len(liveLogs)-30:]
	}
}

// AddLiveLog enregistre un message dans le flux temps réel (systemd et tampon mémoire)
func AddLiveLog(tag, message string) {
	cleanMsg := strings.TrimSpace(message)
	entry := fmt.Sprintf("[%s] %s", tag, cleanMsg)
	log.Println(entry)
}

// GetLiveLogs retourne une copie des derniers logs en direct en mémoire
func GetLiveLogs() []string {
	liveLogsMu.Lock()
	defer liveLogsMu.Unlock()
	cp := make([]string, len(liveLogs))
	copy(cp, liveLogs)
	return cp
}

func GetSystemStatus() SystemStatus {
	numCPUs := runtime.NumCPU()
	
	// 1. Test Internet
	internetActive := checkInternet()

	// 2. Parse RAM
	ramUsed := getRAMUsage()

	// 3. Parse Load Average
	load1 := getLoadAvg()

	// 4. Determine Health Status (based on hardware load only — internet state is informational)
	health := "Excellent"
	if load1 > float64(numCPUs)*0.8 || ramUsed > 85.0 {
		health = "Warning"
	}
	if load1 > float64(numCPUs)*1.2 || ramUsed > 95.0 {
		health = "Critical"
	}

	// 5. Get Logs
	logs := getSystemdLogs()

	return SystemStatus{
		InternetActive: internetActive,
		RAMUsedPercent: ramUsed,
		LoadAverage1:   load1,
		NumCPUs:        numCPUs,
		HealthStatus:   health,
		Logs:           logs,
	}
}

func checkInternet() bool {
	internetCacheMu.Lock()
	defer internetCacheMu.Unlock()

	if time.Since(lastInternetCheck) < 30*time.Second {
		return cachedInternetActive
	}

	// Tester plusieurs endpoints en parallèle — un seul suffit pour confirmer la connexion
	endpoints := []string{
		"http://www.wikipedia.org",
		"http://1.1.1.1",       // Cloudflare DNS (IP directe, pas de DNS requis)
		"http://connectivitycheck.gstatic.com/generate_204", // Google connectivity check
	}

	type result struct{ ok bool }
	ch := make(chan result, len(endpoints))

	for _, url := range endpoints {
		go func(u string) {
			client := http.Client{Timeout: 3 * time.Second}
			resp, err := client.Head(u)
			if err == nil {
				resp.Body.Close()
				ch <- result{true}
			} else {
				ch <- result{false}
			}
		}(url)
	}

	// Dès qu'un endpoint répond positivement, on valide
	lastInternetCheck = time.Now()
	for i := 0; i < len(endpoints); i++ {
		if r := <-ch; r.ok {
			cachedInternetActive = true
			return true
		}
	}
	cachedInternetActive = false
	return false
}

func getRAMUsage() float64 {
	data, err := ioutil.ReadFile("/proc/meminfo")
	if err != nil {
		// Fallback non-Linux (mock ou memoire allouee par Go)
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.Alloc) / (32 * 1024 * 1024) * 100.0 // simulation
	}
	lines := strings.Split(string(data), "\n")
	var memTotal, memAvailable float64
	for _, line := range lines {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				memTotal, _ = strconv.ParseFloat(fields[1], 64)
			}
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				memAvailable, _ = strconv.ParseFloat(fields[1], 64)
			}
		}
	}
	if memTotal > 0 {
		return ((memTotal - memAvailable) / memTotal) * 100.0
	}
	return 0.0
}

func getLoadAvg() float64 {
	data, err := ioutil.ReadFile("/proc/loadavg")
	if err != nil {
		return 0.0
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 1 {
		load1, _ := strconv.ParseFloat(fields[0], 64)
		return load1
	}
	return 0.0
}

func getSystemdLogs() string {
	liveLogsMu.Lock()
	if len(liveLogs) > 0 {
		cp := strings.Join(liveLogs, "\n")
		liveLogsMu.Unlock()
		return cp
	}
	liveLogsMu.Unlock()

	cmd := exec.Command("journalctl", "--user", "-u", "pixel.service", "-n", "15", "--no-pager")
	output, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(output))
	if err != nil || len(outStr) == 0 || outStr == "-- No entries --" || strings.Contains(outStr, "No entries") {
		return "En attente d'événements..."
	}
	logStr := string(output)
	logStr = strings.ReplaceAll(logStr, "cachyos-x8664", "machine-hote")
	logStr = strings.ReplaceAll(logStr, "CachyOS", "système")
	logStr = strings.ReplaceAll(logStr, "cachyos", "système")
	return logStr
}
