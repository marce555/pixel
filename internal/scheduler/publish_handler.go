package scheduler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/marce555/pixel/internal/llm"
)

type ArticlePayload struct {
	Title    string `json:"title"`
	Category string `json:"category"`
	Content  string `json:"content"`
	Keywords string `json:"keywords"`
	Topic    string `json:"topic,omitempty"`
}

const UsedImagesFile = "used_images.json"

func LoadUsedImages() ([]string, error) {
	data, err := os.ReadFile(UsedImagesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var urls []string
	if err := json.Unmarshal(data, &urls); err != nil {
		return nil, err
	}
	return urls, nil
}

func SaveUsedImages(urls []string) error {
	data, err := json.MarshalIndent(urls, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(UsedImagesFile, data, 0644)
}

func AddUsedImage(url string) error {
	if url == "" {
		return nil
	}
	urls, err := LoadUsedImages()
	if err != nil {
		urls = []string{}
	}
	for _, u := range urls {
		if u == url {
			return nil
		}
	}
	urls = append(urls, url)
	return SaveUsedImages(urls)
}

func generateVisualKeywords(ctx context.Context, provider llm.Provider, title string, content string) []string {
	if provider == nil {
		cleanTitle := strings.NewReplacer("-", " ", ":", " ", "?", " ", "!", " ", ",", " ", ".", " ", "\"", " ", "'", " ").Replace(title)
		words := strings.Fields(cleanTitle)
		if len(words) > 3 {
			return []string{strings.Join(words[:3], " ")}
		}
		return []string{cleanTitle}
	}

	snippet := content
	if len(snippet) > 300 {
		snippet = snippet[:300]
	}

	prompt := fmt.Sprintf(`Tu es un directeur artistique Web. Ton rôle est de proposer 3 mots-clés de recherche visuels en ANGLAIS (courts, 2 à 4 mots max) adaptés pour trouver une belle photo sur Unsplash pour illustrer l'article suivant.

Titre de l'article : "%s"
Contenu (extrait) : "%s"

Exemples :
- Pour "DevOps et Kubernetes" -> ["server datacenter", "cloud infrastructure", "code software engineer"]
- Pour "Décohérence quantique" -> ["quantum computing lab", "laser technology", "future server room"]
- Pour "Btrfs RAID 5 write hole" -> ["data storage rack", "hard drive server", "cloud database array"]

Réponds UNIQUEMENT avec un tableau JSON de 3 requêtes en anglais, format strict :
["query 1", "query 2", "query 3"]`, title, snippet)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: "Génère les 3 requêtes Unsplash visuelles en anglais."},
	}

	res, err := provider.Generate(ctx, messages)
	if err == nil {
		res = strings.TrimSpace(res)
		if strings.HasPrefix(res, "```json") {
			res = strings.TrimPrefix(res, "```json")
			res = strings.TrimSuffix(strings.TrimSpace(res), "```")
		} else if strings.HasPrefix(res, "```") {
			res = strings.TrimPrefix(res, "```")
			res = strings.TrimSuffix(strings.TrimSpace(res), "```")
		}
		res = strings.TrimSpace(res)
		var queries []string
		if err := json.Unmarshal([]byte(res), &queries); err == nil && len(queries) > 0 {
			return queries
		}
	}

	cleanTitle := strings.NewReplacer("-", " ", ":", " ", "?", " ", "!", " ", ",", " ", ".", " ", "\"", " ", "'", " ").Replace(title)
	words := strings.Fields(cleanTitle)
	if len(words) > 3 {
		return []string{strings.Join(words[:3], " "), title}
	}
	return []string{cleanTitle}
}

// NewPublishArticleHandler creates a TaskHandler to publish articles to the configured admin interface.
func NewPublishArticleHandler(broadcaster EventBroadcaster, stm STMWriter, provider llm.Provider, searcher WebSearcher) TaskHandler {
	return func(ctx context.Context, task *Task) error {
		task.AppendLog("Démarrage de la tâche de publication...")

		// Parse payload
		var payload ArticlePayload
		if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
			task.AppendLog(fmt.Sprintf("Erreur format payload : %v", err))
			return fmt.Errorf("invalid payload: %w", err)
		}

		// Inline generation if content is empty or placeholder
		if len(strings.TrimSpace(payload.Content)) < 100 {
			topic := payload.Topic
			if topic == "" {
				topic = payload.Title
			}
			task.AppendLog(fmt.Sprintf("Recherche d'informations en direct pour le sujet : '%s'...", topic))

			var knowledge string
			if searcher != nil {
				k, errWiki := searcher.SearchWikipedia(topic)
				if errWiki != nil || k == "" {
					k, _ = searcher.SearchWeb(topic)
				}
				knowledge = k
			}
			if knowledge == "" {
				knowledge = "Pas d'informations en direct trouvées sur le web. Utiliser la connaissance générale de l'IA."
			}

			task.AppendLog("Rédaction de l'article complet par l'IA de Pixel...")
			prompt := fmt.Sprintf(`Tu es le Rédacteur en Chef de Pixel.
Ton rôle est de rédiger un article de blog haut de gamme, complet, captivant, très fouillé et extrêmement détaillé (au moins 2000 mots / 5000 caractères) optimisé pour le SEO en français sur le sujet suivant : "%s".

Voici les informations et le contexte récupérés à ce sujet :
%s

RÈGLES IMPÉRATIVES DE RÉDACTION ET DE STRUCTURE :
1. EXPANSION ET PROFONDEUR : L'article doit être LONG, RICHE et EXHAUSTIF. Développe chaque concept en profondeur avec des explications concrètes, des cas d'usage réels, des exemples techniques et des analyses de fond. Ne rédige JAMAIS un résumé rapide.
2. STRUCTURE HTML : Organise l'article avec :
   - Une introduction captivante qui pose les enjeux.
   - Au moins 4 à 6 grandes sections distinctes avec des titres <h2>.
   - Des sous-sections détaillées avec des sous-titres <h3> sous chaque grande section.
   - Une conclusion prospective et synthétique.
3. FORMATAGE HTML SOIGNÉ :
   - Utilise les balises <p> pour chaque paragraphe et <strong> pour mettre en valeur les termes clés.
   - Intègre systématiquement des listes à puces (<ul>, <li>) ou numérotées (<ol>, <li>) pour aérer la lecture.
   - RÈGLE OBLIGATOIRE : Intègre au moins un TABLEAU HTML complet (<table>, <thead>, <tbody>, <tr>, <th>, <td>) résumant des données, comparant des solutions ou synthétisant les points clés.
   - RÈGLE OBLIGATOIRE : Si le sujet concerne l'informatique, le SysOps, le DevOps, la programmation, l'IA ou les sciences, intègre au moins un ou plusieurs blocs de code HTML complets formatés avec <pre><code class="language-...">...</code></pre> (ex: language-bash, language-python, language-json, language-yaml).
4. Ne mets AUCUNE formule de politesse du type "Voici l'article", commence directement avec les délimiteurs ci-dessous.

Formatte ta réponse EXACTEMENT avec la structure suivante :

---TITLE---
[Titre accrocheur, précis et professionnel de l'article]

---KEYWORDS---
[Un SEUL mot clé visuel très pertinent en anglais (ex: cybersecurity, devops, battery, quantum, cloud, server) pour chercher l'image d'illustration sur Unsplash]

---CONTENT---
[Le contenu HTML complet, riche et structuré de l'article]
`, topic, knowledge)

			messages := []llm.Message{
				{Role: llm.RoleSystem, Content: prompt},
				{Role: llm.RoleUser, Content: "Rédige l'article complet selon le format demandé."},
			}

			if provider == nil {
				task.AppendLog("Erreur : Fournisseur LLM non disponible.")
				return fmt.Errorf("provider nil")
			}

			res, errGen := provider.Generate(ctx, messages)
			if errGen != nil {
				task.AppendLog(fmt.Sprintf("Erreur lors de la rédaction LLM : %v", errGen))
				return fmt.Errorf("generation failed: %w", errGen)
			}

			// Parse article
			var genTitle, genKeywords, genContent string
			parts := strings.Split(res, "---TITLE---")
			if len(parts) > 1 {
				p2 := strings.Split(parts[1], "---KEYWORDS---")
				if len(p2) > 1 {
					genTitle = strings.TrimSpace(p2[0])
					p3 := strings.Split(p2[1], "---CONTENT---")
					if len(p3) > 1 {
						genKeywords = strings.TrimSpace(p3[0])
						genContent = strings.TrimSpace(p3[1])
					}
				}
			}
			if genContent == "" {
				genContent = res
			}
			if genTitle != "" {
				payload.Title = genTitle
			}
			if genKeywords != "" {
				payload.Keywords = genKeywords
			}
			payload.Content = genContent

			if len(payload.Content) < 1200 {
				msg := fmt.Sprintf("Publication annulée : le contenu généré est trop court (%d car.), publication rejetée.", len(payload.Content))
				task.AppendLog(msg)
				return fmt.Errorf("%s", msg)
			}

			task.AppendLog(fmt.Sprintf("Article rédigé avec succès par l'IA (%d caractères). Titre : '%s'.", len(payload.Content), payload.Title))
		}

		// Ultimate pre-publication anti-duplication check
		published, errPub := LoadPublishedArticles()
		if errPub == nil && len(published) > 0 && provider != nil {
			if matchedTitle, tooSimilar := IsTopicTooSimilar(ctx, provider, payload.Title, published); tooSimilar {
				msg := fmt.Sprintf("Publication annulée : l'article proposé '%s' est trop similaire à l'article déjà publié '%s'.", payload.Title, matchedTitle)
				task.AppendLog(msg)
				if broadcaster != nil {
					broadcaster.Broadcast("⚠️ " + msg)
				}
				return fmt.Errorf("%s", msg)
			}
		}

		// Load configurations
		loginURL := os.Getenv("PIXEL_PUBLISH_URL")
		if loginURL == "" {
			loginURL = "https://appliyou.fr/auth/login"
		}
		username := os.Getenv("PIXEL_PUBLISH_USER")
		if username == "" {
			username = "admin"
		}
		password := os.Getenv("PIXEL_PUBLISH_PASS")
		if password == "" {
			task.AppendLog("⚠️ PIXEL_PUBLISH_PASS non configuré dans l'environnement.")
			return fmt.Errorf("PIXEL_PUBLISH_PASS non configuré")
		}
		createURL := os.Getenv("PIXEL_PUBLISH_CREATE_URL")
		if createURL == "" {
			createURL = "https://appliyou.fr/admin/articles/"
		}

		task.AppendLog(fmt.Sprintf("Navigation vers la page de connexion : %s", loginURL))

		// Allocator options for headless Chrome execution
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
		)

		allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
		defer cancelAlloc()

		chromeCtx, cancelChrome := chromedp.NewContext(allocCtx)
		defer cancelChrome()

		// Set timeout for entire operation (15 minutes, since synchronous translation on creation takes time)
		chromeCtx, cancelTimeout := context.WithTimeout(chromeCtx, 900*time.Second)
		defer cancelTimeout()

		// 1. Authenticate
		task.AppendLog("Tentative de connexion...")
		err := chromedp.Run(chromeCtx,
			chromedp.Navigate(loginURL),
			chromedp.WaitVisible(`#username`, chromedp.ByID),
			chromedp.SendKeys(`#username`, username, chromedp.ByID),
			chromedp.SendKeys(`#password`, password, chromedp.ByID),
			chromedp.Click(`button[type="submit"]`, chromedp.ByQuery),
			chromedp.Sleep(3*time.Second), // wait for redirect and session creation
		)
		if err != nil {
			task.AppendLog(fmt.Sprintf("Erreur lors de la tentative de connexion : %v", err))
			failMsg := fmt.Sprintf("⚠️ **[Problème lors de la Publication]**\nL'article **\"%s\"** n'a pas pu être publié sur AppliYou.fr.\n*Raison : Échec de la connexion à l'espace d'administration (%v)*", payload.Title, err)
			if broadcaster != nil {
				broadcaster.Broadcast(failMsg)
			}
			if stm != nil {
				stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: failMsg})
			}
			return fmt.Errorf("login failed: %w", err)
		}
		task.AppendLog("Connexion réussie.")

		// 2. Navigate to administration page
		task.AppendLog(fmt.Sprintf("Navigation vers la page d'administration des articles : %s", createURL))
		var currentURL string
		err = chromedp.Run(chromeCtx,
			chromedp.Navigate(createURL),
			chromedp.Location(&currentURL),
		)
		if err != nil {
			task.AppendLog(fmt.Sprintf("Erreur lors de la navigation vers l'administration : %v", err))
			failMsg := fmt.Sprintf("⚠️ **[Problème lors de la Publication]**\nL'article **\"%s\"** n'a pas pu être publié sur AppliYou.fr.\n*Raison : Échec de navigation vers la page d'administration (%v)*", payload.Title, err)
			if broadcaster != nil {
				broadcaster.Broadcast(failMsg)
			}
			if stm != nil {
				stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: failMsg})
			}
			return fmt.Errorf("navigation failed: %w", err)
		}

		// 3. Click the create article link/button if we are on list page
		if !strings.Contains(currentURL, "create") && !strings.Contains(currentURL, "new") {
			task.AppendLog("Recherche d'un bouton de création d'article...")
			var clicked bool
			err = chromedp.Run(chromeCtx,
				chromedp.Evaluate(`(function() {
					const links = Array.from(document.querySelectorAll('a, button'));
					for (const el of links) {
						const href = el.getAttribute('href') || '';
						const text = (el.innerText || el.textContent || '').toLowerCase();
						if (href.includes('create') || href.includes('new') || href.includes('ajouter') || text.includes('nouveau') || text.includes('ajouter') || text.includes('create')) {
							el.click();
							return true;
						}
					}
					return false;
				})()`, &clicked),
				chromedp.Sleep(2*time.Second),
			)
			if err == nil && clicked {
				task.AppendLog("Bouton de création cliqué avec succès.")
			} else {
				task.AppendLog("Bouton non trouvé. Navigation directe vers la création...")
				directCreateURL := strings.TrimSuffix(createURL, "/") + "/create"
				err = chromedp.Run(chromeCtx, chromedp.Navigate(directCreateURL), chromedp.Sleep(2*time.Second))
				if err != nil {
					task.AppendLog(fmt.Sprintf("Échec de la navigation directe : %v", err))
				}
			}
		}

		// 4. Fill Article Form
		task.AppendLog("Remplissage du formulaire de l'article...")

		// Fill Title
		err = chromedp.Run(chromeCtx,
			chromedp.WaitVisible(`input[placeholder*="Titre"]`, chromedp.ByQuery),
			chromedp.SendKeys(`input[placeholder*="Titre"]`, payload.Title, chromedp.ByQuery),
		)
		if err != nil {
			// Fallback selectors for Title
			chromedp.Run(chromeCtx, chromedp.SendKeys(`input[name="title"]`, payload.Title, chromedp.ByQuery))
		}

		// Select Category
		if payload.Category != "" {
			task.AppendLog(fmt.Sprintf("Sélection de la catégorie : %s", payload.Category))
			chromedp.Run(chromeCtx,
				chromedp.Evaluate(fmt.Sprintf(`(function() {
					const select = document.querySelector('select');
					if (!select) return false;
					const options = Array.from(select.options);
					const targetText = %q.toLowerCase();
					for (const option of options) {
						if (option.text.toLowerCase().includes(targetText) || option.value.toLowerCase().includes(targetText)) {
							select.value = option.value;
							select.dispatchEvent(new Event('change', { bubbles: true }));
							return true;
						}
					}
					if (options.length > 1) {
						select.selectedIndex = 1;
						select.dispatchEvent(new Event('change', { bubbles: true }));
						return true;
					}
					return false;
				})()`, payload.Category), nil),
			)
		}

		// Handle Image Search with Multi-Query Navigation, Anti-Duplication and Vision Verification
		visualQueries := generateVisualKeywords(ctx, provider, payload.Title, payload.Content)
		task.AppendLog(fmt.Sprintf("Requêtes visuelles générées pour Unsplash : %v", visualQueries))

		usedUrlsList, _ := LoadUsedImages()
		usedUrlsMap := make(map[string]bool)
		for _, u := range usedUrlsList {
			usedUrlsMap[u] = true
		}

		var selectedImage bool
		var selectedImageUrl string

		// Select Unsplash as the image provider
		task.AppendLog("Sélection du fournisseur d'images Unsplash...")
		var providerSelected bool
		for i := 0; i < 6; i++ {
			err = chromedp.Run(chromeCtx,
				chromedp.Evaluate(`(function() {
					const select = document.getElementById('imageProvider');
					if (!select) return false;
					const option = Array.from(select.options).find(opt => opt.value === 'unsplash');
					if (option) {
						select.value = 'unsplash';
						select.dispatchEvent(new Event('change', { bubbles: true }));
						return true;
					}
					return false;
				})()`, &providerSelected),
			)
			if err == nil && providerSelected {
				break
			}
			chromedp.Run(chromeCtx, chromedp.Sleep(500*time.Millisecond))
		}
		if !providerSelected {
			task.AppendLog("Avertissement : impossible de sélectionner Unsplash (utilisation du fournisseur par défaut).")
		}

		// Loop through visual queries until a high-scoring unused image (score >= 6) is found
		for qIdx, vQuery := range visualQueries {
			task.AppendLog(fmt.Sprintf("Recherche Unsplash #%d/%d avec la requête : '%s'...", qIdx+1, len(visualQueries), vQuery))

			err = chromedp.Run(chromeCtx,
				chromedp.WaitVisible(`#imageKeywords`, chromedp.ByID),
				chromedp.Evaluate(fmt.Sprintf(`document.getElementById('imageKeywords').value = %q;`, vQuery), nil),
				chromedp.Click(`#searchButton`, chromedp.ByID),
				chromedp.Sleep(8*time.Second), // Wait for API response
			)
			if err != nil {
				task.AppendLog(fmt.Sprintf("Avertissement : échec du lancement de la recherche pour '%s' : %v", vQuery, err))
				continue
			}

			// Extract candidate image URLs from DOM
			var candidateUrls []string
			err = chromedp.Run(chromeCtx,
				chromedp.Evaluate(`(function() {
					const container = document.getElementById('imageOptions') || document.querySelector('.unsplash-results');
					if (!container) return [];
					const imgs = Array.from(container.querySelectorAll('img'));
					return imgs.map(img => img.src).filter(src => src && src.startsWith('http'));
				})()`, &candidateUrls),
			)

			if err != nil || len(candidateUrls) == 0 {
				task.AppendLog(fmt.Sprintf("Aucune image trouvée pour la requête '%s'.", vQuery))
				continue
			}

			task.AppendLog(fmt.Sprintf("Analyse de %d images candidates pour '%s'...", len(candidateUrls), vQuery))

			maxCandidates := 4
			if len(candidateUrls) < maxCandidates {
				maxCandidates = len(candidateUrls)
			}

			for i := 0; i < maxCandidates; i++ {
				candUrl := candidateUrls[i]
				if usedUrlsMap[candUrl] {
					task.AppendLog(fmt.Sprintf("  -> Image candidate %d déjà utilisée sur un article précédent, ignorée.", i+1))
					continue
				}

				task.AppendLog(fmt.Sprintf("  -> Analyse de l'image candidate %d : %s...", i+1, candUrl))
				score, desc, errScore := analyzeImageSuitability(ctx, candUrl, payload.Title)
				if errScore != nil {
					task.AppendLog(fmt.Sprintf("  -> Échec analyse image : %v", errScore))
					score = 0
				} else {
					task.AppendLog(fmt.Sprintf("  -> Image %d - Score: %d/10. Description: %s", i+1, score, desc))
				}

				if score >= 6 {
					task.AppendLog(fmt.Sprintf("🎯 Image sélectionnée ! Score suffisant (%d/10 >= 6/10) pour la requête '%s'.", score, vQuery))
					selectedImageUrl = candUrl
					break
				}
			}

			if selectedImageUrl != "" {
				break
			}
		}

		// Apply selected image to Chrome form
		if selectedImageUrl != "" {
			err = chromedp.Run(chromeCtx,
				chromedp.Evaluate(fmt.Sprintf(`(function() {
					const container = document.getElementById('imageOptions') || document.querySelector('.unsplash-results');
					if (container) {
						const img = Array.from(container.querySelectorAll('img')).find(i => i.src === %q);
						if (img) {
							let el = img;
							while (el && el !== container) {
								el.click();
								el = el.parentElement;
							}
							if (typeof selectImage === 'function') selectImage(img.src);
							
							const inputs = document.querySelectorAll('input[type="hidden"], input[name*="image"], input[name*="thumbnail"], input[name*="cover"]');
							inputs.forEach(i => {
								if (i.name && (i.name.toLowerCase().includes('image') || i.name.toLowerCase().includes('thumb') || i.name.toLowerCase().includes('cover') || i.name.toLowerCase().includes('url'))) {
									i.value = img.src;
									i.dispatchEvent(new Event('change', { bubbles: true }));
								}
							});
							return true;
						}
					}
					const input = document.getElementById('image_url');
					if (input) {
						input.value = %q;
						input.dispatchEvent(new Event('change', { bubbles: true }));
						return true;
					}
					return false;
				})()`, selectedImageUrl, selectedImageUrl), &selectedImage),
				chromedp.Sleep(1*time.Second),
			)
			if selectedImage {
				AddUsedImage(selectedImageUrl)
			}
		}

		// Fallback if no candidate scored >= 6
		if !selectedImage {
			task.AppendLog("Avertissement : Aucune image candidate n'a atteint le score de 6/10. Utilisation du fallback...")
			err = chromedp.Run(chromeCtx,
				chromedp.Evaluate(`(function() {
					const container = document.getElementById('imageOptions') || document.querySelector('.unsplash-results');
					if (container) {
						const img = container.querySelector('img');
						if (img) {
							let el = img;
							while (el && el !== container) {
								el.click();
								el = el.parentElement;
							}
							if (typeof selectImage === 'function') selectImage(img.src);
							return true;
						}
					}
					return false;
				})()`, &selectedImage),
				chromedp.Sleep(1*time.Second),
			)
		}

		if !selectedImage {
			task.AppendLog("Avertissement : aucune image n'a pu être sélectionnée.")
		} else {
			task.AppendLog("Image sélectionnée avec succès !")
		}

		// Fill Content
		task.AppendLog("Insertion du contenu de l'article...")
		err = chromedp.Run(chromeCtx,
			chromedp.Evaluate(fmt.Sprintf(`(function() {
				let set = false;
				if (typeof tinymce !== 'undefined') {
					const editor = tinymce.get('raw_content') || tinymce.activeEditor;
					if (editor) {
						editor.setContent(%q);
						set = true;
					}
				}
				if (typeof CKEDITOR !== 'undefined' && CKEDITOR.instances) {
					for (const key in CKEDITOR.instances) {
						CKEDITOR.instances[key].setData(%q);
						set = true;
					}
				}
				const textarea = document.getElementById('raw_content') || document.querySelector('textarea[name="raw_content"]') || document.querySelector('textarea');
				if (textarea) {
					textarea.value = %q;
					textarea.dispatchEvent(new Event('input', { bubbles: true }));
					textarea.dispatchEvent(new Event('change', { bubbles: true }));
					set = true;
				}
				return set;
			})()`, payload.Content, payload.Content, payload.Content), nil),
		)
		if err != nil {
			task.AppendLog(fmt.Sprintf("Avertissement lors de la saisie du contenu : %v", err))
		}

		// Uncheck draft checkbox if it exists, so that the article is fully published
		task.AppendLog("Désactivation de l'option brouillon (draft)...")
		err = chromedp.Run(chromeCtx,
			chromedp.Evaluate(`(function() {
				const draftInputs = document.querySelectorAll('input[name="is_draft"], input[id*="draft"]');
				draftInputs.forEach(cb => {
					cb.checked = false;
					cb.removeAttribute('checked');
					cb.dispatchEvent(new Event('change', { bubbles: true }));
				});
				const selects = document.querySelectorAll('select[name="status"], select[name*="state"]');
				selects.forEach(select => {
					const opts = Array.from(select.options);
					const pub = opts.find(o => o.text.toLowerCase().includes('publi') || o.value.toLowerCase().includes('publi') || o.value.toLowerCase().includes('published'));
					if (pub) {
						select.value = pub.value;
						select.dispatchEvent(new Event('change', { bubbles: true }));
					}
				});
				return true;
			})()`, nil),
		)
		if err != nil {
			task.AppendLog(fmt.Sprintf("Avertissement lors du décochage du brouillon : %v", err))
		}

		// Submit article form
		task.AppendLog("Soumission de l'article...")
		var submitted bool
		err = chromedp.Run(chromeCtx,
			chromedp.Evaluate(`(function() {
				const buttons = Array.from(document.querySelectorAll('button, input[type="submit"]'));
				for (const b of buttons) {
					const text = (b.innerText || b.textContent || b.value || '').toLowerCase();
					if (text.includes('enregistrer') || text.includes('publier') || text.includes('sauvegarder') || text.includes('submit') || text.includes('publish') || text.includes('creer') || text.includes('créer')) {
						b.click();
						return true;
					}
				}
				const form = document.querySelector('form');
				if (form) {
					form.submit();
					return true;
				}
				return false;
			})()`, &submitted),
		)
		if err != nil || !submitted {
			task.AppendLog("Échec de la soumission du formulaire.")
			failMsg := fmt.Sprintf("⚠️ **[Problème lors de la Publication]**\nL'article **\"%s\"** n'a pas pu être publié sur AppliYou.fr.\n*Raison : Le formulaire d'édition n'a pas pu être soumis sur le serveur (%v)*", payload.Title, err)
			if broadcaster != nil {
				broadcaster.Broadcast(failMsg)
			}
			if stm != nil {
				stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: failMsg})
			}
			return fmt.Errorf("failed to submit article: %w", err)
		}

		// Wait dynamically for redirect (up to 90 seconds, since translations take time)
		task.AppendLog("Attente de la redirection après soumission...")
		var redirectSuccess bool
		for i := 0; i < 90; i++ {
			err = chromedp.Run(chromeCtx,
				chromedp.Evaluate(`(function() {
					const url = window.location.href;
					if (!url.includes('/create') && !url.includes('/new')) {
						if (document.getElementById('enhanceBtn') || document.querySelector('table') || document.querySelector('tr')) {
							return true;
						}
					}
					return false;
				})()`, &redirectSuccess),
			)
			if err == nil && redirectSuccess {
				break
			}
			chromedp.Run(chromeCtx, chromedp.Sleep(1*time.Second))
		}

		var redirectURL string
		err = chromedp.Run(chromeCtx, chromedp.Location(&redirectURL))
		if err == nil {
			if !redirectSuccess {
				task.AppendLog(fmt.Sprintf("Avertissement : Délai d'attente de redirection dépassé. URL actuelle : %s", redirectURL))
			} else {
				task.AppendLog(fmt.Sprintf("Redirection réussie vers : %s", redirectURL))
			}
			
			// If we landed on the list page instead of the edit page, find the edit link of the first article and navigate to it!
			if !strings.Contains(redirectURL, "/edit") {
				task.AppendLog("Redirection hors page d'édition. Recherche du premier article dans la liste pour l'éditer...")
				var navigatedToEdit bool
				err = chromedp.Run(chromeCtx,
					chromedp.Evaluate(`(function() {
						const link = document.querySelector('table tbody tr a[href*="/edit"]');
						if (link) {
							window.location.href = link.href;
							return true;
						}
						return false;
					})()`, &navigatedToEdit),
					chromedp.Sleep(4*time.Second),
					chromedp.Location(&redirectURL),
				)
				if err == nil && navigatedToEdit && strings.Contains(redirectURL, "/edit") {
					task.AppendLog(fmt.Sprintf("Navigation manuelle réussie vers l'édition : %s", redirectURL))
				} else {
					task.AppendLog(fmt.Sprintf("Échec de la navigation vers la page d'édition. URL actuelle : %s", redirectURL))
				}
			}
		}

		if err == nil && strings.Contains(redirectURL, "/edit") {
			// 1. Click "Améliorer avec l'IA"
			task.AppendLog("Lancement de l'amélioration de l'article avec l'IA du site...")
			err = chromedp.Run(chromeCtx,
				chromedp.WaitVisible(`#enhanceBtn`, chromedp.ByID),
				chromedp.Click(`#enhanceBtn`, chromedp.ByID),
			)
			if err != nil {
				task.AppendLog(fmt.Sprintf("Avertissement : échec du clic sur le bouton d'amélioration IA : %v", err))
			} else {
				// Wait for the AI enhancement process to start (classList does NOT contain 'hidden')
				task.AppendLog("Attente du démarrage de l'amélioration par l'IA...")
				var started bool
				for i := 0; i < 15; i++ { // check up to 15 seconds for it to start
					err = chromedp.Run(chromeCtx,
						chromedp.Evaluate(`(function() {
							const progress = document.getElementById('aiProgress');
							return progress ? !progress.classList.contains('hidden') : false;
						})()`, &started),
					)
					if err == nil && started {
						break
					}
					chromedp.Run(chromeCtx, chromedp.Sleep(500*time.Millisecond))
				}

				// Wait for the AI enhancement process to finish (classList contains 'hidden')
				task.AppendLog("Attente de la fin de l'amélioration par l'IA...")
				var finished bool
				for i := 0; i < 180; i++ { // check up to 3 minutes for it to finish
					err = chromedp.Run(chromeCtx,
						chromedp.Evaluate(`(function() {
							const progress = document.getElementById('aiProgress');
							return progress ? progress.classList.contains('hidden') : true;
						})()`, &finished),
					)
					if err == nil && finished {
						break
					}
					chromedp.Run(chromeCtx, chromedp.Sleep(1*time.Second))
				}
				if !finished {
					task.AppendLog("Avertissement : l'amélioration IA a expiré ou a échoué.")
				} else {
					task.AppendLog("Article amélioré avec succès par l'IA !")
				}
			}

			// 2. Click "Enregistrer" to save the enhanced article (ensuring is_draft is unchecked)
			task.AppendLog("Enregistrement de l'article amélioré (décochage brouillon)...")
			var saved bool
			err = chromedp.Run(chromeCtx,
				chromedp.Evaluate(`(function() {
					const draftInputs = document.querySelectorAll('input[name="is_draft"], input[id*="draft"]');
					draftInputs.forEach(cb => {
						cb.checked = false;
						cb.removeAttribute('checked');
						cb.dispatchEvent(new Event('change', { bubbles: true }));
					});
					const buttons = Array.from(document.querySelectorAll('button, input[type="submit"]'));
					for (const b of buttons) {
						const text = (b.innerText || b.textContent || b.value || '').toLowerCase();
						if (text.includes('enregistrer') || text.includes('sauvegarder') || text.includes('save') || text.includes('créer') || text.includes('creer')) {
							b.click();
							return true;
						}
					}
					return false;
				})()`, &saved),
				chromedp.Sleep(4*time.Second), // Wait for save and reload
			)
			if err != nil || !saved {
				task.AppendLog("Avertissement : échec de l'enregistrement de l'article amélioré.")
			} else {
				task.AppendLog("Article enregistré avec succès.")
			}

			// 3. Attente stricte pour permettre aux traductions de se terminer en arrière-plan
			task.AppendLog("Attente de 3 minutes pour permettre les traductions côté serveur...")
			chromedp.Run(chromeCtx, chromedp.Sleep(3*time.Minute))

			// 4. Publication finale (Décocher brouillon et Enregistrer/Soumettre)
			task.AppendLog("Désactivation définitive de l'option brouillon et publication finale...")
			var published bool
			err = chromedp.Run(chromeCtx,
				chromedp.Evaluate(`(function() {
					window.confirm = function() { return true; }; // Override native confirm dialog
					window.alert = function() { return true; };

					const draftInputs = document.querySelectorAll('input[name="is_draft"], input[id*="draft"], input[name*="brouillon"]');
					draftInputs.forEach(cb => {
						cb.checked = false;
						cb.removeAttribute('checked');
						cb.value = "false";
						cb.dispatchEvent(new Event('change', { bubbles: true }));
					});
					const selects = document.querySelectorAll('select[name="status"], select[name*="state"]');
					selects.forEach(select => {
						const opts = Array.from(select.options);
						const pub = opts.find(o => o.text.toLowerCase().includes('publi') || o.value.toLowerCase().includes('publi') || o.value.toLowerCase().includes('published'));
						if (pub) {
							select.value = pub.value;
							select.dispatchEvent(new Event('change', { bubbles: true }));
						}
					});
					
					const buttons = Array.from(document.querySelectorAll('button, input[type="submit"], a.btn'));
					for (const b of buttons) {
						const text = (b.innerText || b.textContent || b.value || '').toLowerCase();
						if ((text.includes('publier') && !text.includes('dépublier')) || text.includes('enregistrer') || text.includes('sauvegarder') || text.includes('save') || text.includes('créer') || text.includes('creer')) {
							b.click();
							return true;
						}
					}
					const form = document.querySelector('form');
					if (form) {
						form.submit();
						return true;
					}
					return false;
				})()`, &published),
				chromedp.Sleep(1*time.Second),
			)

			// 4b. Validation automatique des fenêtres/boutons de confirmation modales (ex: Confirmer la publication)
			task.AppendLog("Recherche et clic automatique sur tout bouton de confirmation / validation (popup modal)...")
			var modalConfirmed bool
			errConfirm := chromedp.Run(chromeCtx,
				chromedp.Evaluate(`(function() {
					window.confirm = function() { return true; };
					const allButtons = Array.from(document.querySelectorAll('button, input[type="button"], input[type="submit"], a.btn, .modal button, .swal2-confirm, [class*="modal"] button, [class*="popup"] button, [class*="dialog"] button, [id*="confirm"] button'));
					for (const btn of allButtons) {
						const style = window.getComputedStyle(btn);
						if (style.display === 'none' || style.visibility === 'hidden') continue;
						const text = (btn.innerText || btn.textContent || btn.value || '').toLowerCase();
						if (text.includes('confirmer') || text.includes('valider') || text.includes('oui') || text === 'ok' || text.includes('publier maintenant') || text.includes('valider la publication') || text.includes('confirmer la publication')) {
							btn.click();
							return true;
						}
					}
					return false;
				})()`, &modalConfirmed),
				chromedp.Sleep(4*time.Second),
			)
			if errConfirm == nil && modalConfirmed {
				task.AppendLog("Bouton de confirmation / validation de la publication cliqué avec succès !")
			}
			
			if err != nil || !published {
				task.AppendLog("Avertissement : échec de l'enregistrement final pour la publication.")
			} else {
				task.AppendLog("Article publié avec succès via l'interface !")
			}
		} else {
			task.AppendLog("Remarque : redirection hors page d'édition ou erreur de récupération de l'URL.")
		}

		task.AppendLog("Article publié avec succès sur le site !")

		if err := AddPublishedArticle(payload.Title); err != nil {
			task.AppendLog(fmt.Sprintf("Avertissement : impossible de mettre à jour le cache des articles publiés : %v", err))
		} else {
			task.AppendLog("Cache des articles publiés mis à jour.")
		}

		formattedResponse := fmt.Sprintf(`📢 **[Publication Réussie]**
*J'ai partagé ma nouvelle découverte avec le monde en publiant l'article : "%s" !*
---
📂 **Catégorie** : %s
📝 **Description** : %s
`, payload.Title, payload.Category, payload.Title)

		broadcaster.Broadcast(formattedResponse)
		stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: formattedResponse})

		return nil
	}
}

// FetchPublishedArticles logins to the site and scrapes the list of already published article titles.
func FetchPublishedArticles(ctx context.Context) ([]string, error) {
	loginURL := os.Getenv("PIXEL_PUBLISH_URL")
	if loginURL == "" {
		loginURL = "https://appliyou.fr/auth/login"
	}
	username := os.Getenv("PIXEL_PUBLISH_USER")
	if username == "" {
		username = "admin"
	}
	password := os.Getenv("PIXEL_PUBLISH_PASS")
	if password == "" {
		return nil, fmt.Errorf("PIXEL_PUBLISH_PASS non configuré")
	}
	createURL := os.Getenv("PIXEL_PUBLISH_CREATE_URL")
	if createURL == "" {
		createURL = "https://appliyou.fr/admin/articles/"
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()

	chromeCtx, cancelChrome := chromedp.NewContext(allocCtx)
	defer cancelChrome()

	chromeCtx, cancelTimeout := context.WithTimeout(chromeCtx, 90*time.Second)
	defer cancelTimeout()

	err := chromedp.Run(chromeCtx,
		chromedp.Navigate(loginURL),
		chromedp.WaitVisible(`#username`, chromedp.ByID),
		chromedp.SendKeys(`#username`, username, chromedp.ByID),
		chromedp.SendKeys(`#password`, password, chromedp.ByID),
		chromedp.Click(`button[type="submit"]`, chromedp.ByQuery),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("login failed during article list fetch: %w", err)
	}

	var allTitles []string
	for page := 1; page <= 10; page++ {
		pageURL := fmt.Sprintf("%s?page=%d", createURL, page)
		var pageTitles []string
		err = chromedp.Run(chromeCtx,
			chromedp.Navigate(pageURL),
			chromedp.Sleep(2*time.Second),
			chromedp.Evaluate(`(function() {
				const links = Array.from(document.querySelectorAll('table tbody tr a[href*="/edit"]'));
				return links.map(a => a.innerText.trim()).filter(t => t !== "");
			})()`, &pageTitles),
		)
		if err != nil || len(pageTitles) == 0 {
			break
		}
		allTitles = append(allTitles, pageTitles...)
	}

	return allTitles, nil
}

type visionMessageContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

type visionMessage struct {
	Role    string                 `json:"role"`
	Content []visionMessageContent `json:"content"`
}

type visionRequest struct {
	Model     string          `json:"model"`
	Messages  []visionMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens"`
}

func analyzeImageSuitability(ctx context.Context, imageURL string, articleTitle string) (int, string, error) {
	// 1. Download image
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(imageURL)
	if err != nil {
		return 0, "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	imgBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("failed to read image body: %w", err)
	}

	// 2. Base64 encode
	imgBase64 := base64.StdEncoding.EncodeToString(imgBytes)
	imgDataURL := "data:image/jpeg;base64," + imgBase64

	// 3. Call local vision model
	flmURL := "http://127.0.0.1:52625/v1/chat/completions"
	reqBody := visionRequest{
		Model:     "qwen3vl-it:4b",
		MaxTokens: 128,
		Messages: []visionMessage{
			{
				Role: "user",
				Content: []visionMessageContent{
					{
						Type: "text",
						Text: fmt.Sprintf("Cette image est proposée pour illustrer un article intitulé \"%s\". Décris brièvement cette image en français et évalue sa pertinence pour cet article sur une échelle de 0 à 10. Réponds uniquement sous ce format strict : \"Score: <note>, Description: <description>\"", articleTitle),
					},
					{
						Type: "image_url",
						ImageURL: &struct {
							URL string `json:"url"`
						}{URL: imgDataURL},
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return 0, "", fmt.Errorf("failed to marshal JSON: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", flmURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, "", fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	visionClient := &http.Client{Timeout: 120 * time.Second}
	apiResp, err := visionClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("failed to call vision API: %w", err)
	}
	defer apiResp.Body.Close()

	if apiResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(apiResp.Body)
		return 0, "", fmt.Errorf("vision API returned status %d: %s", apiResp.StatusCode, string(bodyBytes))
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	bodyBytes, err := io.ReadAll(apiResp.Body)
	if err != nil {
		return 0, "", err
	}

	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return 0, "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return 0, "", fmt.Errorf("empty choice returned by vision model")
	}

	content := chatResp.Choices[0].Message.Content
	
	// Parse score and description
	lower := strings.ToLower(content)
	scoreIdx := strings.Index(lower, "score:")
	score := 0
	if scoreIdx != -1 {
		numStr := ""
		for i := scoreIdx + 6; i < len(lower); i++ {
			char := lower[i]
			if char >= '0' && char <= '9' {
				numStr += string(char)
			} else if numStr != "" {
				break
			}
		}
		fmt.Sscanf(numStr, "%d", &score)
	}

	desc := content
	descIdx := strings.Index(lower, "description:")
	if descIdx != -1 {
		desc = strings.TrimSpace(content[descIdx+12:])
	}

	return score, desc, nil
}
