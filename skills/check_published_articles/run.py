import sys
import os
import json
import urllib.request
import urllib.error

def clean_text(text):
    return text.lower().strip()

def find_published_articles_file():
    # Try current directory relatives and absolute paths
    possible_paths = [
        os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", "published_articles.json")),
        "/home/marceloc/Documents/Pixel/published_articles.json",
        "published_articles.json"
    ]
    for p in possible_paths:
        if os.path.exists(p):
            return p
    return None

def main():
    try:
        query = sys.argv[1] if len(sys.argv) > 1 else ""
        query_clean = clean_text(query)
        
        # Ignorer les mots génériques de requête de vérification
        generic_queries = ["check", "all", "vérification", "verification", "statut", "status", "publication", "publications", ""]
        is_specific_search = query_clean not in generic_queries and len(query_clean) > 3

        articles_file = find_published_articles_file()
        published_titles = []
        if articles_file and os.path.exists(articles_file):
            try:
                with open(articles_file, 'r', encoding='utf-8') as f:
                    published_titles = json.load(f)
            except Exception as e:
                print(f"⚠️ Erreur lors de la lecture de {articles_file}: {e}")

        # Récupérer l'état des tâches actives depuis l'API locale Pixel
        active_tasks = []
        try:
            req = urllib.request.Request("http://127.0.0.1:8080/api/tasks")
            with urllib.request.urlopen(req, timeout=3) as resp:
                if resp.status == 200:
                    tasks_data = json.loads(resp.read().decode('utf-8'))
                    for t in tasks_data:
                        task_type = t.get("type", "")
                        if "publish" in task_type or "article" in task_type or "depublish" in task_type:
                            active_tasks.append(t)
        except Exception:
            pass  # L'API backend n'est peut-être pas active en mode script isolé

        print("=== RAPPORT FACTUEL DE VÉRIFICATION DES PUBLICATIONS ===")
        print(f"Nombre total d'articles enregistrés comme publiés : {len(published_titles)}")
        
        if is_specific_search:
            print(f"\nRecherche spécifique demandée : '{query}'")
            matched_articles = [t for t in published_titles if query_clean in clean_text(t) or any(w in clean_text(t) for w in query_clean.split() if len(w) > 3)]
            if matched_articles:
                print("✅ Article(s) correspondant(s) trouvé(s) sur AppliYou.fr :")
                for m in matched_articles:
                    print(f"  - {m}")
            else:
                print(f"❌ AUCUN article correspondant à '{query}' n'a été publié sur AppliYou.fr.")

        if published_titles:
            print("\nDerniers articles publiés sur AppliYou.fr :")
            for i, title in enumerate(reversed(published_titles[-5:]), 1):
                print(f"  {i}. {title}")
        else:
            print("\nAucun article publié trouvé dans le cache local (published_articles.json est vide).")

        if active_tasks:
            print("\nÉtat des tâches de publication récentes / en cours :")
            for t in active_tasks:
                status = t.get("status", "unknown")
                name = t.get("name", "sans nom")
                task_id = t.get("id", "")
                print(f"  - [{status.upper()}] {name} (ID: {task_id})")
        else:
            print("\nAucune tâche de publication en cours dans le gestionnaire de tâches.")

    except Exception as e:
        print(f"Erreur lors de l'exécution du skill check_published_articles : {e}", file=sys.stderr)

if __name__ == '__main__':
    main()
