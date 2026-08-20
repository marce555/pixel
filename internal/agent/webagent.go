package agent

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// WebAgent handles external internet searches (Wikipedia).
type WebAgent struct{}

func NewWebAgent() *WebAgent {
	return &WebAgent{}
}

// SearchWikipedia searches Wikipedia for a topic and returns the intro extract.
func (w *WebAgent) SearchWikipedia(topic string) (string, error) {
	apiURL := "https://fr.wikipedia.org/w/api.php"

	params := url.Values{}
	params.Add("action", "query")
	params.Add("format", "json")
	params.Add("prop", "extracts")
	params.Add("exintro", "true")
	params.Add("explaintext", "true")
	params.Add("generator", "search")
	params.Add("gsrsearch", topic)
	params.Add("gsrlimit", "1")

	reqURL := fmt.Sprintf("%s?%s", apiURL, params.Encode())

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("erreur HTTP creation requete: %w", err)
	}
	// Wikipedia exige un User-Agent valide
	req.Header.Set("User-Agent", "PixelAutonomousAgent/1.0 (contact@pixel.local)")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("erreur HTTP: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("erreur lecture body: %w", err)
	}

	var result struct {
		Query struct {
			Pages map[string]struct {
				Title   string `json:"title"`
				Extract string `json:"extract"`
				Index   int    `json:"index"`
			} `json:"pages"`
		} `json:"query"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("erreur parsing JSON: %w", err)
	}

	var bestPage struct {
		Title   string
		Extract string
		Index   int
	}
	hasPage := false
	for _, page := range result.Query.Pages {
		if !hasPage || page.Index < bestPage.Index {
			bestPage.Title = page.Title
			bestPage.Extract = page.Extract
			bestPage.Index = page.Index
			hasPage = true
		}
	}

	if hasPage {
		return fmt.Sprintf("Titre: %s\nRésumé: %s", bestPage.Title, bestPage.Extract), nil
	}

	return "", fmt.Errorf("aucun résultat trouvé pour '%s'", topic)
}

type RSS struct {
	Channel Channel `xml:"channel"`
}
type Channel struct {
	Items []Item `xml:"item"`
}
type Item struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
}

// SearchNews searches trusted RSS feeds for a topic and returns the latest headlines.
func (w *WebAgent) SearchNews(topic string) (string, error) {
	trustedFeeds := []struct {
		Name string
		URL  string
	}{
		{"FranceInfo", "https://www.francetvinfo.fr/titres.rss"},
		{"L'Équipe", "https://www.lequipe.fr/rss/actu_rss.xml"},
		{"Le Monde", "https://www.lemonde.fr/rss/une.xml"},
	}

	type FeedItem struct {
		Source string
		Title  string
		Desc   string
	}
	var allItems []FeedItem

	client := &http.Client{Timeout: 5 * time.Second}

	for _, feed := range trustedFeeds {
		req, err := http.NewRequest("GET", feed.URL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "PixelAutonomousAgent/1.0 (contact@pixel.local)")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		body, err := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}

		var rss RSS
		if err := xml.Unmarshal(body, &rss); err == nil {
			for _, item := range rss.Channel.Items {
				allItems = append(allItems, FeedItem{Source: feed.Name, Title: item.Title, Desc: item.Description})
			}
		}
	}

	if len(allItems) == 0 {
		return "", fmt.Errorf("impossible de récupérer les flux RSS de confiance")
	}

	// Nettoyage et préparation des mots-clés du sujet
	topicClean := strings.ToLower(topic)
	words := strings.FieldsFunc(topicClean, func(r rune) bool {
		return r == ' ' || r == '-' || r == '\'' || r == ',' || r == '.' || r == '/' || r == '(' || r == ')'
	})
	
	var importantWords []string
	for _, w := range words {
		// Ignorer les mots trop communs ou liés à notre date future
		if len(w) > 2 && w != "des" && w != "les" && w != "une" && w != "qui" && w != "que" && w != "pour" && w != "sur" && w != "dans" && w != "avec" && w != "juin" && w != "2026" && w != "aujourd'hui" && w != "actualités" && w != "actualite" && w != "news" {
			importantWords = append(importantWords, w)
		}
	}

	var matchedItems []FeedItem
	if len(importantWords) > 0 {
		for _, item := range allItems {
			content := strings.ToLower(item.Title + " " + item.Desc)
			matchCount := 0
			for _, w := range importantWords {
				if strings.Contains(content, w) {
					matchCount++
				}
			}
			// Si au moins un mot clé pertinent est trouvé
			if matchCount > 0 {
				matchedItems = append(matchedItems, item)
			}
		}
	}

	var builder strings.Builder
	limit := 8

	if len(matchedItems) > 0 {
		builder.WriteString(fmt.Sprintf("Actualités fiables trouvées spécifiquement pour '%s' :\n", topic))
		if len(matchedItems) < limit {
			limit = len(matchedItems)
		}
		for i := 0; i < limit; i++ {
			desc := matchedItems[i].Desc
			// Nettoyer un peu le HTML de la description si présent
			reTags := regexp.MustCompile(`(?is)<.*?>`)
			desc = reTags.ReplaceAllString(desc, "")
			builder.WriteString(fmt.Sprintf("- [%s] %s\n  %s\n", matchedItems[i].Source, matchedItems[i].Title, desc))
		}
	} else {
		builder.WriteString(fmt.Sprintf("Aucune actualité fiable trouvée spécifiquement pour '%s'. Voici les gros titres d'information générale certifiée :\n", topic))
		if len(allItems) < limit {
			limit = len(allItems)
		}
		for i := 0; i < limit; i++ {
			desc := allItems[i].Desc
			reTags := regexp.MustCompile(`(?is)<.*?>`)
			desc = reTags.ReplaceAllString(desc, "")
			builder.WriteString(fmt.Sprintf("- [%s] %s\n  %s\n", allItems[i].Source, allItems[i].Title, desc))
		}
		builder.WriteString("\n[INSTRUCTION SYSTÈME STRICTE] À la fin de ta réponse, tu DOIS proposer à l'utilisateur : 'Je n'ai pas trouvé de résultats précis dans les journaux officiels. Souhaitez-vous que je lance une recherche web plus approfondie sur ce sujet ?'")
	}

	return builder.String(), nil
}

type GoogleSearchResult struct {
	Items []struct {
		Title   string `json:"title"`
		Snippet string `json:"snippet"`
		Link    string `json:"link"`
	} `json:"items"`
}

// SearchWeb searches the general web. It prioritizes Google Custom Search if API keys are configured,
// and falls back to DuckDuckGo Instant Answer API otherwise.
func (w *WebAgent) SearchWeb(query string) (string, error) {
	googleAPIKey := os.Getenv("GOOGLE_API_KEY")
	googleCX := os.Getenv("GOOGLE_CX")

	if googleAPIKey != "" && googleCX != "" {
		// 1. Google Custom Search
		apiURL := "https://www.googleapis.com/customsearch/v1"
		params := url.Values{}
		params.Add("key", googleAPIKey)
		params.Add("cx", googleCX)
		params.Add("q", query)
		params.Add("num", "5")

		reqURL := fmt.Sprintf("%s?%s", apiURL, params.Encode())
		req, err := http.NewRequest("GET", reqURL, nil)
		if err != nil {
			return "", fmt.Errorf("erreur creation requete Google Search: %w", err)
		}

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("erreur HTTP Google Search: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			body, err := ioutil.ReadAll(resp.Body)
			if err == nil {
				var result GoogleSearchResult
				if err := json.Unmarshal(body, &result); err == nil && len(result.Items) > 0 {
					var builder strings.Builder
					builder.WriteString(fmt.Sprintf("Résultats de recherche Google pour '%s':\n", query))
					for _, item := range result.Items {
						builder.WriteString(fmt.Sprintf("- Titre: %s\n  Description: %s\n  Lien: %s\n", item.Title, item.Snippet, item.Link))
					}
					return builder.String(), nil
				}
			}
		}
	}

	// 2. Fallback to DuckDuckGo HTML Search (highly reliable, no API key needed, no captchas)
	apiURL := "https://html.duckduckgo.com/html/"
	params := url.Values{}
	params.Add("q", query)

	reqURL := apiURL

	req, err := http.NewRequest("POST", reqURL, strings.NewReader(params.Encode()))
	if err != nil {
		return "", fmt.Errorf("erreur creation requete DuckDuckGo: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("erreur HTTP DuckDuckGo: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("erreur lecture body DuckDuckGo: %w", err)
	}

	body := string(bodyBytes)

	// Regex to match titles, links, and snippets
	reResult := regexp.MustCompile(`(?s)<h2 class="result__title">\s*<a rel="nofollow" class="result__a" href="([^"]+)">(.*?)</a>.*?<a class="result__snippet"[^>]*>(.*?)</a>`)
	
	matches := reResult.FindAllStringSubmatch(body, 5)
	if len(matches) == 0 {
		return "", fmt.Errorf("aucun résultat web trouvé pour '%s'", query)
	}

	reTags := regexp.MustCompile(`<[^>]*>`)
	cleanHTML := func(s string) string {
		s = reTags.ReplaceAllString(s, "")
		return strings.TrimSpace(html.UnescapeString(s))
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Résultats de recherche Web pour '%s':\n", query))

	for _, match := range matches {
		if len(match) == 4 {
			link := match[1]
			title := cleanHTML(match[2])
			snippet := cleanHTML(match[3])
			builder.WriteString(fmt.Sprintf("- Titre: %s\n  Description: %s\n  Lien: %s\n", title, snippet, link))
		}
	}

	return builder.String(), nil
}

func isExactMatch(query, title string) bool {
	queryClean := strings.ToLower(query)
	titleClean := strings.ToLower(title)

	// Split query into words
	words := strings.FieldsFunc(queryClean, func(r rune) bool {
		return r == ' ' || r == '-' || r == '\'' || r == ',' || r == '.' || r == '/' || r == '(' || r == ')' || r == '[' || r == ']' || r == '|'
	})

	matchedCount := 0
	importantWordsCount := 0

	for _, w := range words {
		// Ignore short or common stop words
		if len(w) <= 2 || w == "des" || w == "les" || w == "une" || w == "qui" || w == "que" || w == "de" || w == "la" || w == "le" || w == "du" || w == "en" || w == "pour" {
			continue
		}
		importantWordsCount++
		if strings.Contains(titleClean, w) {
			matchedCount++
		}
	}

	if importantWordsCount == 0 {
		return true // No important words to match
	}

	// If we matched at least 75% of the important words
	matchRatio := float64(matchedCount) / float64(importantWordsCount)
	return matchRatio >= 0.75
}

func (w *WebAgent) ValidateTitleWithWikipedia(query string) string {
	result, err := w.SearchWikipedia(query)
	if err != nil || result == "" {
		return ""
	}
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Titre: ") {
			title := strings.TrimPrefix(line, "Titre: ")
			title = strings.TrimSpace(title)
			if title != "" {
				return title
			}
		}
	}
	return ""
}

// ResolveMusicURL searches YouTube and returns the target URL, the video title, and whether it is a direct track link.
func (w *WebAgent) ResolveMusicURL(songQuery string) (string, string, bool, error) {
	query := strings.TrimSpace(songQuery)
	if query == "" {
		return "", "", false, fmt.Errorf("requête de musique vide")
	}

	targetURL := ""
	videoTitle := ""

	// 1. Try direct scraping of YouTube's search results page (ultra-fast, native, exact)
	targetURL, videoTitle = w.ScrapeYouTubeDirect(query)

	// Check if the title returned matches query exactly.
	// If it doesn't match exactly, or if no video was found, validate the query with Wikipedia and run a new search with the corrected title.
	exact := false
	if targetURL != "" && videoTitle != "" {
		exact = isExactMatch(query, videoTitle)
	}

	if !exact {
		if targetURL != "" {
			fmt.Printf("[WebAgent] Titre YouTube '%s' ne correspond pas exactement à la requête '%s'. Validation via Wikipédia...\n", videoTitle, query)
		} else {
			fmt.Printf("[WebAgent] Aucun titre trouvé directement sur YouTube pour '%s'. Validation via Wikipédia...\n", query)
		}
		wikiTitle := w.ValidateTitleWithWikipedia(query)
		if wikiTitle != "" && strings.ToLower(wikiTitle) != strings.ToLower(query) {
			fmt.Printf("[WebAgent] Titre validé par Wikipédia : '%s'. Nouvelle recherche YouTube...\n", wikiTitle)
			newURL, newTitle := w.ScrapeYouTubeDirect(wikiTitle)
			if newURL != "" {
				targetURL = newURL
				videoTitle = newTitle
			}
		}
	}

	// 2. Fallback to Yahoo search if direct scraping did not return a direct link
	if targetURL == "" {
		fmt.Println("[WebAgent] Le scraper en direct n'a pas retourné de lien. Repli sur la recherche Yahoo...")
		searchQuery := fmt.Sprintf("site:youtube.com %s", query)
		results, err := w.SearchWeb(searchQuery)
		
		if err == nil && results != "" {
			// Look for a link containing watch?v=, playlist?list= or shared link in the results
			lines := strings.Split(results, "\n")
			for _, line := range lines {
				if strings.Contains(line, "Lien: ") && (strings.Contains(line, "youtube.com/watch?v=") || strings.Contains(line, "youtu.be/") || strings.Contains(line, "youtube.com/playlist?list=")) {
					targetURL = strings.TrimPrefix(line, "  Lien: ")
					targetURL = strings.TrimSpace(targetURL)

					// If the URL contains a playlist list ID, route it directly to the YouTube Music watch player.
					if strings.Contains(targetURL, "list=") {
						playlistID := ""
						u, parseErr := url.Parse(targetURL)
						if parseErr == nil {
							playlistID = u.Query().Get("list")
						}
						if playlistID == "" {
							re := regexp.MustCompile(`[?&]list=([^&]+)`)
							matches := re.FindStringSubmatch(targetURL)
							if len(matches) >= 2 {
								playlistID = matches[1]
							}
						}

						if playlistID != "" {
							targetURL = fmt.Sprintf("https://music.youtube.com/watch?list=%s", playlistID)
							fmt.Printf("[WebAgent] Conversion de playlist en YouTube Music watch player URL : %s\n", targetURL)
						}
					}
					break
				}
			}
		}
	}

	// 3. If no direct link was parsed, fallback to opening YouTube search results page
	var isDirect bool
	if targetURL == "" {
		targetURL = fmt.Sprintf("https://www.youtube.com/results?search_query=%s", url.QueryEscape(query))
		fmt.Printf("[WebAgent] Aucun lien YouTube direct trouvé. Ouverture de la page de recherche : %s\n", targetURL)
	} else {
		isDirect = true
		// Inject autoplay=1 parameter to force browser auto-play
		if strings.Contains(targetURL, "?") {
			targetURL += "&autoplay=1"
		} else {
			targetURL += "?autoplay=1"
		}
		fmt.Printf("[WebAgent] Lien YouTube direct trouvé avec autoplay : %s. Lancement de la lecture...\n", targetURL)
	}

	return targetURL, videoTitle, isDirect, nil
}

// PlayMusic searches for a song and opens it in the default browser using xdg-open.
func (w *WebAgent) PlayMusic(songQuery string) (string, error) {
	fmt.Printf("[WebAgent] Tentative de lecture de musique pour : '%s'\n", songQuery)

	targetURL, videoTitle, isDirect, err := w.ResolveMusicURL(songQuery)
	if err != nil {
		return "", err
	}

	// 3. Essayer de lancer la lecture automatique via mpv en arrière-plan (mode sans vidéo, insensible aux blocages d'autoplay)
	path := os.Getenv("PATH")
	localBin := "/home/marceloc/Documents/Pixel/bin"
	newPath := localBin + ":" + path

	args := []string{"--no-video", "--input-ipc-server=/tmp/mpvsocket_webagent"}
	if strings.Contains(targetURL, "list=") {
		args = append(args, "--ytdl-raw-options=yes-playlist=")
	}
	args = append(args, targetURL)

	mpvCmd := exec.Command("mpv", args...)
	mpvCmd.Env = append(os.Environ(), "PATH="+newPath)

	if err := mpvCmd.Start(); err == nil {
		fmt.Printf("[WebAgent] Lancement réussi de mpv en arrière-plan pour : %s (%s)\n", targetURL, videoTitle)
		if isDirect {
			return fmt.Sprintf("J'ai trouvé la chanson **'%s'** sur YouTube et je l'ai lancée automatiquement en arrière-plan avec **mpv** (mode sans vidéo). Bonne écoute ! 🎶\n*(Lien : %s)*", videoTitle, targetURL), nil
		}
		return fmt.Sprintf("J'ai ouvert les résultats de recherche YouTube directement dans le lecteur **mpv** en arrière-plan. 🎵\n*(Lien : %s)*", targetURL), nil
	}

	fmt.Printf("[WebAgent] Échec du lancement de mpv en arrière-plan. Repli sur le navigateur...\n")

	// 4. Repli : Ouvrir l'URL dans le navigateur par défaut du système via xdg-open
	cmd := exec.Command("xdg-open", targetURL)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("impossible d'ouvrir le navigateur via xdg-open : %w", err)
	}

	// Double-sécurité pour le navigateur : tenter de forcer la lecture et le volume via playerctl
	if isDirect {
		go func() {
			for i := 1; i <= 3; i++ {
				time.Sleep(1500 * time.Millisecond)
				fmt.Printf("[WebAgent] Tentative d'activation MPRIS #%d (volume 100%% + play)...\n", i)
				exec.Command("playerctl", "volume", "1.0").Run()
				exec.Command("playerctl", "play").Run()
			}
		}()
	}

	if isDirect {
		return fmt.Sprintf("J'ai trouvé la piste directe sur YouTube et je l'ai lancée avec l'autoplay actif dans ton navigateur. Bonne écoute ! 🎶\n*(Lien : %s)*", targetURL), nil
	}
	return fmt.Sprintf("Je n'ai pas trouvé de piste directe à 100%%, alors j'ai ouvert la page de recherche YouTube pour '%s' dans ton navigateur. 🎵", songQuery), nil
}

// ControlMedia controls the mpv media player via its IPC socket (/tmp/mpvsocket).
// Falls back to playerctl if mpv socket is unavailable.
func (w *WebAgent) ControlMedia(command string) (string, error) {
	fmt.Printf("[WebAgent] Commande média reçue : '%s'\n", command)

	var mpvCmd string
	var actionDesc string

	switch strings.ToLower(command) {
	case "play", "resume":
		mpvCmd = `{"command": ["set_property", "pause", false]}`
		actionDesc = "repris la lecture de ta musique"
	case "pause":
		mpvCmd = `{"command": ["set_property", "pause", true]}`
		actionDesc = "mis la musique en pause"
	case "toggle":
		mpvCmd = `{"command": ["cycle", "pause"]}`
		actionDesc = "basculé la lecture (lecture/pause)"
	case "next", "skip":
		mpvCmd = `{"command": ["playlist-next"]}`
		actionDesc = "passé à la piste suivante"
	case "prev", "previous", "back":
		mpvCmd = `{"command": ["playlist-prev"]}`
		actionDesc = "revenu à la piste précédente"
	case "stop":
		mpvCmd = `{"command": ["quit"]}`
		actionDesc = "arrêté la musique"
	default:
		return "", fmt.Errorf("commande média inconnue : %s", command)
	}

	// Try all mpv IPC sockets (used when music launched via Scheduler)
	sockets, _ := filepath.Glob("/tmp/mpvsocket_*")
	successCount := 0
	for _, socket := range sockets {
		conn, err := net.DialTimeout("unix", socket, 500*time.Millisecond)
		if err == nil {
			_, writeErr := conn.Write([]byte(mpvCmd + "\n"))
			conn.Close()
			if writeErr == nil {
				successCount++
			}
		}
	}

	if successCount > 0 {
		fmt.Printf("[WebAgent] Commande IPC mpv envoyée avec succès à %d instances : %s\n", successCount, mpvCmd)
		return fmt.Sprintf("J'ai %s avec succès via le lecteur mpv en arrière-plan. 🎵", actionDesc), nil
	}

	// Fallback: try playerctl (for browser-based playback)
	var playerctlArgs []string
	switch strings.ToLower(command) {
	case "play", "resume":
		playerctlArgs = []string{"play"}
	case "pause":
		playerctlArgs = []string{"pause"}
	case "toggle":
		playerctlArgs = []string{"play-pause"}
	case "next", "skip":
		playerctlArgs = []string{"next"}
	case "prev", "previous", "back":
		playerctlArgs = []string{"previous"}
	case "stop":
		playerctlArgs = []string{"stop"}
	}

	pctlCmd := exec.Command("playerctl", playerctlArgs...)
	if err := pctlCmd.Run(); err == nil {
		return fmt.Sprintf("J'ai %s avec succès via playerctl. 🎵", actionDesc), nil
	}

	// Fallback for stop: kill any active mpv process if running
	if strings.ToLower(command) == "stop" {
		if err := exec.Command("pkill", "-f", "mpv").Run(); err == nil {
			return fmt.Sprintf("J'ai %s avec succès. 🎵", actionDesc), nil
		}
	}

	return "", fmt.Errorf("impossible de contrôler la lecture : ni le socket IPC de mpv (/tmp/mpvsocket) ni playerctl ne sont disponibles. Assure-toi qu'une musique est en cours de lecture via mon gestionnaire de tâches")
}

// ScrapeYouTubeDirect queries YouTube Search page directly (using HTTP GET, acting like curl)
// and extracts the exact playlist ID or video ID of the first organic search result along with its title.
func (w *WebAgent) ScrapeYouTubeDirect(songQuery string) (string, string) {
	fmt.Printf("[WebAgent] Scraping YouTube direct pour : '%s'\n", songQuery)

	searchURL := fmt.Sprintf("https://www.youtube.com/results?search_query=%s", url.QueryEscape(songQuery))
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return "", ""
	}

	// Set a standard desktop browser User-Agent so YouTube returns the rich HTML/JSON page
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[WebAgent] Scraping direct échoué (erreur HTTP) : %v\n", err)
		return "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("[WebAgent] Scraping direct échoué (HTTP %d)\n", resp.StatusCode)
		return "", ""
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", ""
	}

	body := string(bodyBytes)
	queryLower := strings.ToLower(songQuery)
	isPlaylistQuery := strings.Contains(queryLower, "playlist") || strings.Contains(queryLower, "liste") || strings.Contains(queryLower, "hits") || strings.Contains(queryLower, "top 100") || strings.Contains(queryLower, "best of") || strings.Contains(queryLower, "suite de")

	playlistID := ""
	videoID := ""
	videoTitle := ""

	// 1. Extract Playlist ID if it's a playlist/mix query
	if isPlaylistQuery {
		// Attempt A: Look for "playlistId":"..." in raw JSON block inside HTML
		rePlaylistJSON := regexp.MustCompile(`"playlistId":"([a-zA-Z0-9_-]{18,})"`)
		matchesJSON := rePlaylistJSON.FindAllStringSubmatch(body, -1)
		for _, m := range matchesJSON {
			if len(m) >= 2 {
				playlistID = m[1]
				fmt.Printf("[WebAgent] Scraper: Playlist ID trouvé en JSON direct : %s\n", playlistID)
				break
			}
		}

		if playlistID == "" {
			// Attempt B: Look for /playlist?list= in raw HTML
			rePlaylist := regexp.MustCompile(`/playlist\?list=([a-zA-Z0-9_-]{18,})`)
			matches := rePlaylist.FindAllStringSubmatch(body, -1)
			for _, m := range matches {
				if len(m) >= 2 {
					playlistID = m[1]
					fmt.Printf("[WebAgent] Scraper: Playlist ID trouvé en HTML direct : %s\n", playlistID)
					break
				}
			}
		}
	}

	// 2. Extract Video ID (first match) and Title
	// Attempt A: Look for "videoId":"..." inside the search results JSON block
	reVideoJSON := regexp.MustCompile(`"videoId":"([a-zA-Z0-9_-]{11})"`)
	matchIndices := reVideoJSON.FindStringSubmatchIndex(body)
	if len(matchIndices) >= 4 {
		videoID = body[matchIndices[2]:matchIndices[3]]
		fmt.Printf("[WebAgent] Scraper: Video ID trouvé en JSON direct : %s\n", videoID)

		// Find the title text after this videoID
		searchArea := body[matchIndices[1]:]
		if len(searchArea) > 2000 {
			searchArea = searchArea[:2000]
		}
		reTitle := regexp.MustCompile(`"text":"([^"]+)"`)
		titleMatch := reTitle.FindStringSubmatch(searchArea)
		if len(titleMatch) >= 2 {
			videoTitle = titleMatch[1]
			videoTitle = html.UnescapeString(videoTitle)
		}
	}

	if videoID == "" {
		// Attempt B: Look for /watch?v= in raw HTML
		reWatch := regexp.MustCompile(`/watch\?v=([a-zA-Z0-9_-]{11})`)
		matchesWatch := reWatch.FindAllStringSubmatch(body, -1)
		for _, m := range matchesWatch {
			if len(m) >= 2 {
				videoID = m[1]
				fmt.Printf("[WebAgent] Scraper: Video ID trouvé en HTML direct : %s\n", videoID)
				break
			}
		}
	}

	// 3. Assemble and return the most specific URL and its title
	if playlistID != "" && videoID != "" {
		// We have both: perfect watch link for MPV/yt-dlp including dynamic mixes!
		return fmt.Sprintf("https://music.youtube.com/watch?v=%s&list=%s", videoID, playlistID), videoTitle
	} else if playlistID != "" {
		// Playlist only fallback
		return fmt.Sprintf("https://music.youtube.com/watch?list=%s", playlistID), ""
	} else if videoID != "" {
		// Video only fallback
		return fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID), videoTitle
	}

	return "", ""
}

// ReadURLContent fetches the HTML content of a URL and extracts visible text.
func (w *WebAgent) ReadURLContent(targetURL string) (string, error) {
	fmt.Printf("[WebAgent] Lecture du contenu de l'URL : %s\n", targetURL)
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("erreur HTTP lors de la lecture de l'URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("statut HTTP non OK : %d", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("erreur lecture body: %w", err)
	}
	
	body := string(bodyBytes)

	// Remove script and style tags completely
	reScript := regexp.MustCompile(`(?is)<script.*?>.*?</script>`)
	body = reScript.ReplaceAllString(body, " ")
	
	reStyle := regexp.MustCompile(`(?is)<style.*?>.*?</style>`)
	body = reStyle.ReplaceAllString(body, " ")

	// Remove all HTML tags
	reTags := regexp.MustCompile(`(?is)<.*?>`)
	body = reTags.ReplaceAllString(body, " ")

	// Unescape HTML entities
	body = html.UnescapeString(body)

	// Clean up whitespaces
	reSpaces := regexp.MustCompile(`\s+`)
	body = reSpaces.ReplaceAllString(body, " ")
	body = strings.TrimSpace(body)

	// Limit size to avoid overloading LLM
	maxLen := 15000
	if len(body) > maxLen {
		body = body[:maxLen] + "... [TRONQUÉ]"
	}

	return body, nil
}

// ReadURLDynamic uses a headless Chromium browser to render JavaScript before extracting text.
func (w *WebAgent) ReadURLDynamic(targetURL string) (string, error) {
	fmt.Printf("[WebAgent] Navigation dynamique (Headless) vers : %s\n", targetURL)

	// Create context
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	// Set timeout for the whole operation (45 seconds)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	defer cancelTimeout()

	var bodyText string

	// Run tasks
	err := chromedp.Run(ctx,
		// Navigate to URL
		chromedp.Navigate(targetURL),
		// Wait an arbitrary time to let dynamic content load (e.g., flight prices)
		chromedp.Sleep(3*time.Second),
		// Extract innerText of the body
		chromedp.Evaluate(`document.body.innerText`, &bodyText),
	)

	if err != nil {
		return "", fmt.Errorf("erreur chromedp: %w", err)
	}

	// Clean up whitespaces
	reSpaces := regexp.MustCompile(`\s+`)
	body := reSpaces.ReplaceAllString(bodyText, " ")
	body = strings.TrimSpace(body)

	// Limit size to avoid overloading LLM
	maxLen := 15000
	if len(body) > maxLen {
		body = body[:maxLen] + "... [TRONQUÉ]"
	}

	return body, nil
}

// ControlBrowserVisually utilise chromedp sans le mode headless pour ouvrir un navigateur visible.
// Il s'exécute en arrière-plan pour ne pas bloquer l'agent Pixel.
func (w *WebAgent) ControlBrowserVisually(targetURL string) error {
	fmt.Printf("[WebAgent] Ouverture visible du navigateur via chromedp pour : %s\n", targetURL)

	go func() {
		// 1. Configurer chromedp pour qu'il NE SOIT PAS headless (donc visible)
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", false),
			chromedp.Flag("start-maximized", true),
		)

		allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
		defer cancelAlloc()

		// 2. Créer le contexte du navigateur
		ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
		defer cancel()

		// Garde la fenêtre ouverte pendant 30 minutes au maximum pour laisser l'utilisateur regarder la vidéo ou les résultats
		ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Minute)
		defer cancelTimeout()

		// 3. Lancer les actions
		err := chromedp.Run(ctx,
			// Ouvre l'URL spécifiée
			chromedp.Navigate(targetURL),
			// Attendre pour laisser l'utilisateur interagir ou voir la page
			chromedp.Sleep(30*time.Minute),
		)
		
		if err != nil {
			fmt.Printf("[WebAgent] Erreur chromedp visible : %v\n", err)
		}
	}()

	return nil
}

// OpenYouTubeVisually ouvre une recherche ou une vidéo YouTube dans le navigateur visible.
func (w *WebAgent) OpenYouTubeVisually(query string) error {
	targetURL := fmt.Sprintf("https://www.youtube.com/results?search_query=%s", url.QueryEscape(query))
	return w.ControlBrowserVisually(targetURL)
}

// OpenGoogleSearchVisually ouvre une recherche Google dans le navigateur visible.
func (w *WebAgent) OpenGoogleSearchVisually(query string) error {
	searchURL := fmt.Sprintf("https://www.google.com/search?q=%s", url.QueryEscape(query))
	return w.ControlBrowserVisually(searchURL)
}

// OpenWindowsDownloadBrowser ouvre le navigateur visible sur la page de téléchargement officielle Microsoft.
func (w *WebAgent) OpenWindowsDownloadBrowser() error {
	targetURL := "https://www.microsoft.com/software-download/windows11"
	fmt.Printf("[WebAgent] Ouverture du navigateur Chromium sur la page de téléchargement Windows : %s\n", targetURL)
	return w.ControlBrowserVisually(targetURL)
}


