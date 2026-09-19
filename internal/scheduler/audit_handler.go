package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/marce555/pixel/internal/llm"
)

// ArticleAuditResult stores the diagnostic for an audited article.
type ArticleAuditResult struct {
	Title        string   `json:"title"`
	EditURL      string   `json:"edit_url"`
	HasTable     bool     `json:"has_table"`
	HasCodeBlock bool     `json:"has_code_block"`
	Length       int      `json:"length"`
	Issues       []string `json:"issues"`
	Fixed        bool     `json:"fixed"`
	DraftID      string   `json:"draft_id,omitempty"`
	ReviewNotes  string   `json:"review_notes,omitempty"`
}

// NewAuditAndFixArticlesHandler creates a TaskHandler to audit and optionally fix published articles on AppliYou.
func NewAuditAndFixArticlesHandler(broadcaster EventBroadcaster, stm STMWriter, provider llm.Provider, draftManager *DraftManager, reviewer ArticleReviewer) TaskHandler {
	if draftManager == nil {
		draftManager = NewDraftManager("drafts")
	}

	return func(ctx context.Context, task *Task) error {
		task.AppendLog("🔍 Démarrage de la mission d'audit et relecture des publications sur AppliYou...")

		mode := strings.ToLower(strings.TrimSpace(task.Payload))
		auditOnly := strings.Contains(mode, "audit-only") || strings.Contains(mode, "dry-run")
		specificFilter := ""
		if mode != "" && !auditOnly && mode != "all" && mode != "fix" {
			specificFilter = mode
		}

		if auditOnly {
			task.AppendLog("📋 Mode : Audit seul (lecture seule, aucune modification en ligne).")
		} else {
			task.AppendLog("🛠️ Mode : Audit et Correction automatique des publications défectueuses.")
		}

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

		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
		)

		allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
		defer cancelAlloc()

		chromeCtx, cancelChrome := chromedp.NewContext(allocCtx)
		defer cancelChrome()

		chromeCtx, cancelTimeout := context.WithTimeout(chromeCtx, 1800*time.Second)
		defer cancelTimeout()

		// 1. Authenticate
		task.AppendLog("Connexion à l'espace d'administration AppliYou...")
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
		task.AppendLog("Connexion réussie.")

		// 2. Scrape articles list across all pages
		task.AppendLog("Récupération de la liste complète des articles publiés...")
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
						list.push({
							title: title,
							editURL: editLink.href,
							status: (tr.innerText || '').trim()
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
			task.AppendLog("Aucun article trouvé à auditer.")
			return fmt.Errorf("no articles found on administration pages")
		}

		task.AppendLog(fmt.Sprintf("%d articles trouvés sur AppliYou.", len(articles)))

		// 3. Audit each article
		var audited []ArticleAuditResult
		var fixedCount, issueCount int

		for idx, art := range articles {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			if specificFilter != "" && !strings.Contains(strings.ToLower(art.Title), strings.ToLower(specificFilter)) {
				continue
			}

			task.AppendLog(fmt.Sprintf("Audit (%d/%d) : \"%s\"...", idx+1, len(articles), art.Title))

			var extracted struct {
				Title    string `json:"title"`
				Content  string `json:"content"`
				Keywords string `json:"keywords"`
				Category string `json:"category"`
			}

			err = chromedp.Run(chromeCtx,
				chromedp.Navigate(art.EditURL),
				chromedp.Sleep(3*time.Second),
				chromedp.Evaluate(`(function() {
					let content = "";
					if (typeof tinymce !== 'undefined') {
						const editor = tinymce.get('raw_content') || tinymce.activeEditor;
						if (editor) content = editor.getContent();
					}
					if (!content && typeof CKEDITOR !== 'undefined' && CKEDITOR.instances) {
						for (const k in CKEDITOR.instances) {
							content = CKEDITOR.instances[k].getData();
							if (content) break;
						}
					}
					if (!content) {
						const textarea = document.getElementById('raw_content') || document.querySelector('textarea[name="raw_content"]') || document.querySelector('textarea');
						if (textarea) content = textarea.value;
					}
					const titleInput = document.querySelector('input[name="title"], input[placeholder*="Titre"]');
					const keyInput = document.getElementById('imageKeywords');
					const catSelect = document.querySelector('select');
					return {
						title: titleInput ? titleInput.value : "",
						content: content || "",
						keywords: keyInput ? keyInput.value : "",
						category: catSelect ? catSelect.value : "Technologies"
					};
				})()`, &extracted),
			)

			if err != nil {
				task.AppendLog(fmt.Sprintf("  ⚠️ Impossible d'accéder à l'édition de \"%s\" : %v", art.Title, err))
				continue
			}

			content := extracted.Content
			title := extracted.Title
			if title == "" {
				title = art.Title
			}

			// Diagnostic checks
			hasTable := strings.Contains(content, "<table")
			hasCodeBlock := strings.Contains(content, "<pre><code")
			hasMarkdownHeadings := strings.Contains(content, "## ") || strings.Contains(content, "# ")
			hasMarkdownFormatting := strings.Contains(content, "**") || strings.Contains(content, "* **")
			hasHeadings := strings.Contains(content, "<h2") || strings.Contains(content, "<h3")
			trimmed := strings.TrimSpace(content)
			isTruncated := strings.HasSuffix(trimmed, "<") || strings.HasSuffix(trimmed, "</") || strings.HasSuffix(trimmed, "\\") || strings.HasSuffix(trimmed, "TABLE")
			hasRawLatex := strings.Contains(content, "$$") || strings.Contains(content, `\propto`)
			length := len(content)

			var issues []string
			if !hasTable {
				issues = append(issues, "Absence de tableau HTML (<table>)")
			}
			if hasMarkdownHeadings || hasMarkdownFormatting {
				issues = append(issues, "Formatage Markdown brut non converti en HTML (#, ##, **)")
			}
			if !hasHeadings && hasMarkdownHeadings {
				issues = append(issues, "Absence de balises de titres HTML (<h2>, <h3>)")
			}
			if isTruncated {
				issues = append(issues, "Contenu tronqué en fin d'article (balise '<' ou phrase inachevée)")
			}
			if hasRawLatex {
				issues = append(issues, "Formules mathématiques LaTeX ($$) non interprétées")
			}
			if length < 800 {
				issues = append(issues, fmt.Sprintf("Contenu très court (%d caractères)", length))
			}
			isTech := isTechTopic(title, content)
			if isTech && !hasCodeBlock {
				issues = append(issues, "Sujet technique sans bloc de code (<pre><code>)")
			}

			result := ArticleAuditResult{
				Title:        title,
				EditURL:      art.EditURL,
				HasTable:     hasTable,
				HasCodeBlock: hasCodeBlock,
				Length:       length,
				Issues:       issues,
				Fixed:        false,
			}

			if len(issues) == 0 {
				task.AppendLog(fmt.Sprintf("  ✅ Conforme (%d caractères, tableau présent).", length))
				audited = append(audited, result)
				continue
			}

			issueCount++
			task.AppendLog(fmt.Sprintf("  ⚠️ Anomalies détectées (%s).", strings.Join(issues, ", ")))

			// If audit-only or reviewer is nil, don't fix
			if auditOnly || reviewer == nil {
				audited = append(audited, result)
				continue
			}

			// 4. Correction par le ReviewerAgent et mise en cache
			task.AppendLog("  🤖 Lancement de la révision par l'Agent Relecteur...")

			var revTitle, revKeywords, revContent, revNotes string
			var draft *ArticleDraft
			if existing, errFind := draftManager.FindLatestDraftByTitle(title); errFind == nil && existing.ReviewedContent != "" {
				task.AppendLog(fmt.Sprintf("  ⚡ Réutilisation de la révision en cache (Brouillon %s)...", existing.ID))
				draft = existing
				result.DraftID = existing.ID
				revTitle = existing.Title
				revKeywords = existing.Keywords
				revContent = existing.ReviewedContent
				revNotes = existing.ReviewNotes
			} else {
				d, errDraft := draftManager.CreateDraft(title, title, extracted.Category, extracted.Keywords, content)
				if errDraft != nil {
					task.AppendLog(fmt.Sprintf("  ⚠️ Échec de création du brouillon : %v", errDraft))
					continue
				}
				draft = d
				result.DraftID = draft.ID
				_ = draftManager.UpdateDraftStatus(draft.ID, DraftStatusInReview, "Audit & Correction des publications existantes")

				t, k, c, n, errRev := reviewer.Review(ctx, draft)
				if errRev != nil {
					task.AppendLog(fmt.Sprintf("  ⚠️ Échec de la relecture IA : %v", errRev))
					continue
				}
				revTitle, revKeywords, revContent, revNotes = t, k, c, n
			}

			if draft != nil {
				// Ensure revContent is 100% pure semantic HTML with repaired tables and structure
				revContent = CleanToSemanticHTML(revContent, revTitle)
				_ = draftManager.UpdateReviewedContent(draft.ID, revTitle, revKeywords, revContent, revNotes)
				result.ReviewNotes = revNotes

					// 5. Injection du contenu corrigé dans AppliYou
					task.AppendLog("  💾 Réinjection du contenu corrigé dans AppliYou...")
					revJSON, errJSON := json.Marshal(revContent)
					if errJSON != nil {
						task.AppendLog(fmt.Sprintf("  ⚠️ Erreur sérialisation contenu : %v", errJSON))
						continue
					}

					// Rafraîchir/naviguer vers la page d'édition pour garantir une session active et un formulaire prêt
					errNav := chromedp.Run(chromeCtx,
						chromedp.Navigate(art.EditURL),
						chromedp.WaitVisible(`.tox-tinymce`, chromedp.ByQuery),
						chromedp.Sleep(2*time.Second),
					)
					if errNav != nil {
						task.AppendLog(fmt.Sprintf("  ⚠️ Avertissement navigation édition : %v", errNav))
					}

					// Attendre l'initialisation complète de l'éditeur TinyMCE
					for waitInit := 0; waitInit < 15; waitInit++ {
						var ready bool
						_ = chromedp.Run(chromeCtx,
							chromedp.Evaluate(`(function() {
								return (typeof tinymce !== 'undefined' && tinymce.get('raw_content') && tinymce.get('raw_content').initialized);
							})()`, &ready),
						)
						if ready {
							break
						}
						time.Sleep(500 * time.Millisecond)
					}

					var updated bool
					errSet := chromedp.Run(chromeCtx,
						chromedp.Evaluate(fmt.Sprintf(`(function() {
							const contentVal = %s;
							let set = false;
							if (typeof tinymce !== 'undefined') {
								if (tinymce.get('raw_content')) {
									tinymce.get('raw_content').setContent(contentVal);
									tinymce.get('raw_content').save();
									set = true;
								}
								if (tinymce.get('content')) {
									tinymce.get('content').setContent(contentVal);
									tinymce.get('content').save();
									set = true;
								}
								tinymce.triggerSave();
							}
							if (typeof CKEDITOR !== 'undefined' && CKEDITOR.instances) {
								for (const key in CKEDITOR.instances) {
									CKEDITOR.instances[key].setData(contentVal);
									set = true;
								}
							}
							const taRaw = document.getElementById('raw_content') || document.querySelector('textarea[name="raw_content"]');
							if (taRaw) {
								taRaw.value = contentVal;
								taRaw.dispatchEvent(new Event('input', { bubbles: true }));
								taRaw.dispatchEvent(new Event('change', { bubbles: true }));
								set = true;
							}
							const taContent = document.getElementById('content') || document.querySelector('textarea[name="content"]');
							if (taContent) {
								taContent.value = contentVal;
								taContent.dispatchEvent(new Event('input', { bubbles: true }));
								taContent.dispatchEvent(new Event('change', { bubbles: true }));
								set = true;
							}

							// Nettoyage de summary et meta_description si présents pour retirer les # ou **
							const sum = document.getElementById('summary') || document.querySelector('textarea[name="summary"]');
							if (sum && (sum.value.includes('#') || sum.value.includes('**'))) {
								sum.value = sum.value.replace(/#{1,6}\s*/g, '').replace(/\*\*/g, '').trim();
								sum.dispatchEvent(new Event('input', { bubbles: true }));
								sum.dispatchEvent(new Event('change', { bubbles: true }));
								if (typeof tinymce !== 'undefined' && tinymce.get('summary')) {
									tinymce.get('summary').setContent(sum.value);
									tinymce.get('summary').save();
								}
							}
							const meta = document.getElementById('meta_description') || document.querySelector('textarea[name="meta_description"]');
							if (meta && (meta.value.includes('#') || meta.value.includes('**'))) {
								meta.value = meta.value.replace(/#{1,6}\s*/g, '').replace(/\*\*/g, '').trim();
								meta.dispatchEvent(new Event('input', { bubbles: true }));
								meta.dispatchEvent(new Event('change', { bubbles: true }));
							}

							return set;
						})()`, string(revJSON)), &updated),
					)

					var submitted bool
					if errSet == nil && updated {
						task.AppendLog("  🚀 Envoi de la requête de sauvegarde du formulaire...")
						errClick := chromedp.Run(chromeCtx,
							chromedp.Evaluate(`(function() {
								if (typeof tinymce !== 'undefined') tinymce.triggerSave();
								const buttons = Array.from(document.querySelectorAll('button, input[type="submit"]'));
								for (const b of buttons) {
									const text = (b.innerText || b.textContent || b.value || '').toLowerCase();
									if (text.includes('enregistrer') || text.includes('sauvegarder') || text.includes('save') || text.includes('publier')) {
										setTimeout(() => b.click(), 50);
										return true;
									}
								}
								const form = document.querySelector('form');
								if (form) {
									setTimeout(() => form.submit(), 50);
									return true;
								}
								return false;
							})()`, &submitted),
						)
						if errClick == nil && submitted {
							// Wait for form submission and redirect
							task.AppendLog("  ⏳ Attente de la confirmation de mise à jour...")
							time.Sleep(5 * time.Second)
							locCtx, locCancel := context.WithTimeout(chromeCtx, 5*time.Second)
							var currentLoc string
							_ = chromedp.Run(locCtx, chromedp.Location(&currentLoc))
							locCancel()
							if currentLoc != "" && !strings.Contains(currentLoc, "/edit") {
								task.AppendLog(fmt.Sprintf("  ✅ Redirection confirmée vers : %s", currentLoc))
							}
						}
					}

					if submitted {
						result.Fixed = true
						fixedCount++
						_ = draftManager.UpdateDraftStatus(draft.ID, DraftStatusPublished, "Article corrigé avec succès sur AppliYou")
						task.AppendLog(fmt.Sprintf("  ✨ Article \"%s\" corrigé et sauvegardé avec succès en ligne !", title))
					} else {
						task.AppendLog(fmt.Sprintf("  ⚠️ Échec de réinjection en ligne pour \"%s\" (mis à jour: %v, erreur: %v)", title, updated, errSet))
					}
				}

			audited = append(audited, result)
		}

		// 6. Summary Report
		task.AppendLog(fmt.Sprintf("Audit terminé ! %d articles analysés, %d avec anomalies, %d corrigés en ligne.", len(audited), issueCount, fixedCount))

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("🧐 **[Rapport d'Audit de Formatage des Publications]**\n"))
		sb.WriteString(fmt.Sprintf("J'ai audité **%d** articles sur AppliYou.fr :\n", len(audited)))
		sb.WriteString(fmt.Sprintf("- ✅ **Conformes** : %d\n", len(audited)-issueCount))
		sb.WriteString(fmt.Sprintf("- ⚠️ **Anomalies détectées** : %d\n", issueCount))
		if !auditOnly {
			sb.WriteString(fmt.Sprintf("- 🔧 **Corrigés et republiés en ligne** : %d\n", fixedCount))
		}
		sb.WriteString("\n---\n")

		problemCount := 0
		for _, a := range audited {
			if len(a.Issues) > 0 {
				problemCount++
				if problemCount > 8 {
					sb.WriteString(fmt.Sprintf("\n*(... et %d autres articles audités)*\n", issueCount-8))
					break
				}
				statusIcon := "⚠️"
				actionStr := "*Non modifié (audit seul)*"
				if a.Fixed {
					statusIcon = "✨"
					actionStr = fmt.Sprintf("✅ **Corrigé et mis à jour sur AppliYou** *(Brouillon archivé : `%s`)*", a.DraftID)
				}
				sb.WriteString(fmt.Sprintf("%s **%s**\n", statusIcon, a.Title))
				sb.WriteString(fmt.Sprintf("  - Défauts : %s\n", strings.Join(a.Issues, " | ")))
				sb.WriteString(fmt.Sprintf("  - Action : %s\n", actionStr))
				if a.ReviewNotes != "" {
					sb.WriteString(fmt.Sprintf("  - Relecture : *%s*\n", a.ReviewNotes))
				}
				sb.WriteString("\n")
			}
		}

		report := sb.String()
		if broadcaster != nil {
			broadcaster.Broadcast(report)
		}
		if stm != nil {
			stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: report})
		}

		return nil
	}
}

func isTechTopic(title, content string) bool {
	combined := strings.ToLower(title + " " + content)
	techKeywords := []string{
		"linux", "btrfs", "docker", "kubernetes", "devops", "cloud", "serveur", "server",
		"python", "bash", "code", "ia", "intelligence artificielle", "algo", "api", "ssh", "git",
	}
	for _, kw := range techKeywords {
		if strings.Contains(combined, kw) {
			return true
		}
	}
	return false
}
