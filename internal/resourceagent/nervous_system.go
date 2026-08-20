package resourceagent

import (
	"io/ioutil"
	"net/http"
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
)

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
	cmd := exec.Command("journalctl", "--user", "-u", "pixel.service", "-n", "8", "--no-pager")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "Impossible de lire les logs systemd : " + err.Error()
	}
	logStr := string(output)
	logStr = strings.ReplaceAll(logStr, "cachyos-x8664", "machine-hote")
	logStr = strings.ReplaceAll(logStr, "CachyOS", "système")
	logStr = strings.ReplaceAll(logStr, "cachyos", "système")
	return logStr
}
