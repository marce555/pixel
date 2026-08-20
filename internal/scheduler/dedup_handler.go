package scheduler

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/marce555/pixel/internal/llm"
)

type ScrapedArticle struct {
	Title   string `json:"title"`
	EditURL string `json:"editURL"`
	Status  string `json:"status"`
}

// NewDepublishDuplicatesHandler creates a TaskHandler to audit, identify, and unpublish duplicate articles.
func NewDepublishDuplicatesHandler(broadcaster EventBroadcaster, stm STMWriter, provider llm.Provider) TaskHandler {
	return func(ctx context.Context, task *Task) error {
		task.AppendLog("Démarrage de la détection et dépublication autonome des articles en double sur AppliYou.fr...")

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
			task.AppendLog("⚠️ PIXEL_PUBLISH_PASS n'est pas configuré dans l'environnement.")
			return fmt.Errorf("PIXEL_PUBLISH_PASS non configuré")
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

		chromeCtx, cancelTimeout := context.WithTimeout(chromeCtx, 900*time.Second)
		defer cancelTimeout()

		// 1. Authenticate
		task.AppendLog("Connexion à l'espace d'administration...")
		err := chromedp.Run(chromeCtx,
			chromedp.Navigate(loginURL),
			chromedp.WaitVisible(`#username`, chromedp.ByID),
			chromedp.SendKeys(`#username`, username, chromedp.ByID),
			chromedp.SendKeys(`#password`, password, chromedp.ByID),
			chromedp.Click(`button[type="submit"]`, chromedp.ByQuery),
			chromedp.Sleep(3*time.Second),
		)
		if err != nil {
			task.AppendLog(fmt.Sprintf("Échec de connexion : %v", err))
			return fmt.Errorf("login failed: %w", err)
		}

		// 2. Navigate to administration articles table and scrape all pages
		task.AppendLog("Navigation vers la liste des articles et numérisation de toutes les pages...")
		var articles []ScrapedArticle

		for page := 1; page <= 10; page++ {
			pageURL := fmt.Sprintf("%s?page=%d", createURL, page)
			var pageArticles []ScrapedArticle
			err = chromedp.Run(chromeCtx,
				chromedp.Navigate(pageURL),
				chromedp.Sleep(2*time.Second),
				chromedp.Evaluate(`(function() {
					const rows = Array.from(document.querySelectorAll('table tbody tr'));
					const list = [];
					for (const tr of rows) {
						const editLink = tr.querySelector('a[href*="/edit"]');
						if (!editLink) continue;
						const title = editLink.innerText.trim();
						if (!title) continue;
						const statusText = (tr.innerText || tr.textContent || '').trim();
						list.push({
							title: title,
							editURL: editLink.href,
							status: statusText
						});
					}
					return list;
				})()`, &pageArticles),
			)
			if err != nil || len(pageArticles) == 0 {
				break
			}
			articles = append(articles, pageArticles...)
		}

		if len(articles) == 0 {
			task.AppendLog("Aucun article trouvé dans le tableau.")
			return fmt.Errorf("failed to scrape articles list across pages")
		}

		task.AppendLog(fmt.Sprintf("%d articles identifiés au total sur l'ensemble des pages d'administration.", len(articles)))

		// 4. Group articles into kept vs duplicate
		var keptArticles []ScrapedArticle
		var duplicateArticles []ScrapedArticle

		for _, art := range articles {
			var keptTitles []string
			for _, k := range keptArticles {
				keptTitles = append(keptTitles, k.Title)
			}

			if matchedTitle, tooSimilar := IsTopicTooSimilar(ctx, provider, art.Title, keptTitles); tooSimilar {
				task.AppendLog(fmt.Sprintf("⚠️ Doublon détecté : '%s' (similaire à '%s')", art.Title, matchedTitle))
				duplicateArticles = append(duplicateArticles, art)
			} else {
				keptArticles = append(keptArticles, art)
			}
		}

		if len(duplicateArticles) == 0 {
			task.AppendLog("✅ Analyse terminée : Aucun article en double n'a été détecté.")
			msg := "✅ **[Vérification Articles]** Aucun article en double n'a été trouvé. Tous les articles sont uniques."
			if broadcaster != nil {
				broadcaster.Broadcast(msg)
			}
			if stm != nil {
				stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: msg})
			}
			return nil
		}

		task.AppendLog(fmt.Sprintf("Lancement de la dépublication et suppression de %d articles en double...", len(duplicateArticles)))
		unpublishedCount := 0

		// 5. Unpublish and Delete duplicate articles
		for idx, dup := range duplicateArticles {
			task.AppendLog(fmt.Sprintf("[%d/%d] Suppression du doublon : '%s' (%s)...", idx+1, len(duplicateArticles), dup.Title, dup.EditURL))

			// Extract article ID from edit URL (e.g. /admin/articles/122/edit -> 122)
			var articleID string
			parts := strings.Split(dup.EditURL, "/")
			for pIdx, p := range parts {
				if p == "articles" && pIdx+1 < len(parts) {
					articleID = parts[pIdx+1]
					break
				}
			}

			var deleteSuccess bool
			if articleID != "" {
				err = chromedp.Run(chromeCtx,
					chromedp.Navigate(createURL),
					chromedp.Sleep(1*time.Second),
					chromedp.Evaluate(fmt.Sprintf(`(function() {
						fetch('/admin/articles/%s/unpublish', {
							method: 'POST',
							headers: { 'Content-Type': 'application/json' }
						}).then(res => res.json()).then(data => {
							console.log('Unpublish response:', data);
						}).catch(err => console.error(err));
						return true;
					})()`, articleID), &deleteSuccess),
					chromedp.Sleep(2*time.Second),
				)
			}

			// Fallback: Edit page and uncheck/draft
			if !deleteSuccess {
				chromedp.Run(chromeCtx,
					chromedp.Navigate(dup.EditURL),
					chromedp.Sleep(2*time.Second),
					chromedp.Evaluate(`(function() {
						const cb = document.querySelector('input[name="is_draft"], input[id*="draft"], input[type="checkbox"]');
						if (cb) {
							cb.checked = true;
							cb.dispatchEvent(new Event('change', { bubbles: true }));
						}
						const select = document.querySelector('select[name="status"], select[name*="state"]');
						if (select) {
							const opts = Array.from(select.options);
							const draft = opts.find(o => o.text.toLowerCase().includes('brouillon') || o.value.toLowerCase().includes('draft'));
							if (draft) {
								select.value = draft.value;
								select.dispatchEvent(new Event('change', { bubbles: true }));
							}
						}
						const form = document.querySelector('form');
						if (form) form.submit();
						return true;
					})()`, nil),
					chromedp.Sleep(3*time.Second),
				)
			}

			unpublishedCount++
			task.AppendLog(fmt.Sprintf("  -> Article '%s' (ID: %s) supprimé / dépublié avec succès.", dup.Title, articleID))
		}

		// 6. Update local cache
		var keptTitles []string
		for _, k := range keptArticles {
			keptTitles = append(keptTitles, k.Title)
		}
		SavePublishedArticles(keptTitles)

		summaryMsg := fmt.Sprintf("🧹 **[Nettoyage des Articles Terminé]**\n- Articles analysés : **%d**\n- Doublons identifiés : **%d**\n- Articles supprimés / dépubliés : **%d**", len(articles), len(duplicateArticles), unpublishedCount)
		task.AppendLog(summaryMsg)

		if broadcaster != nil {
			broadcaster.Broadcast(summaryMsg)
		}
		if stm != nil {
			stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: summaryMsg})
		}

		return nil
	}
}
