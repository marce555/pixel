#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import os
import sys
import json
import imaplib
import email
from email.header import decode_header
import re
import urllib.request
import urllib.error
import time

# ─────────────────────────────────────────────────────────────────────────────
# Correspondances des onglets Gmail (labels IMAP / requêtes X-GM-RAW)
# ─────────────────────────────────────────────────────────────────────────────
GMAIL_CATEGORIES = {
    "principal":       "category:primary",
    "promotions":      "category:promotions",
    "réseaux sociaux": "category:social",
    "notifications":   "category:updates",
    "forums":          "category:forums",
}

CATEGORY_EMOJI = {
    "Principal":       "📬",
    "Promotions":      "🛒",
    "Réseaux sociaux": "📱",
    "Notifications":   "🔔",
    "Forums":          "💬",
}

# ─────────────────────────────────────────────────────────────────────────────
# Utilitaires de décodage
# ─────────────────────────────────────────────────────────────────────────────

def decode_mime_header(header_value):
    if not header_value:
        return ""
    decoded = decode_header(header_value)
    parts = []
    for text, encoding in decoded:
        if isinstance(text, bytes):
            try:
                parts.append(text.decode(encoding or 'utf-8', errors='ignore'))
            except Exception:
                parts.append(text.decode('latin1', errors='ignore'))
        else:
            parts.append(str(text))
    return "".join(parts)


def get_email_body(msg):
    body = ""
    if msg.is_multipart():
        for part in msg.walk():
            ctype = part.get_content_type()
            cdispo = str(part.get('Content-Disposition'))
            if ctype == 'text/plain' and 'attachment' not in cdispo:
                try:
                    payload = part.get_payload(decode=True)
                    charset = part.get_content_charset() or 'utf-8'
                    body = payload.decode(charset, errors='ignore')
                    break
                except Exception:
                    pass
        if not body:
            for part in msg.walk():
                ctype = part.get_content_type()
                if ctype == 'text/html':
                    try:
                        payload = part.get_payload(decode=True)
                        charset = part.get_content_charset() or 'utf-8'
                        body = payload.decode(charset, errors='ignore')
                        # Supprime toutes les balises HTML et les entités courantes
                        body = re.sub(r'<style[^>]*>.*?</style>', '', body, flags=re.DOTALL | re.IGNORECASE)
                        body = re.sub(r'<script[^>]*>.*?</script>', '', body, flags=re.DOTALL | re.IGNORECASE)
                        body = re.sub(r'<[^>]+>', '', body)
                        body = re.sub(r'&nbsp;', ' ', body)
                        body = re.sub(r'&[a-zA-Z]+;', '', body)
                        break
                    except Exception:
                        pass
    else:
        try:
            payload = msg.get_payload(decode=True)
            charset = msg.get_content_charset() or 'utf-8'
            body = payload.decode(charset, errors='ignore')
            if '<html' in body.lower():
                body = re.sub(r'<style[^>]*>.*?</style>', '', body, flags=re.DOTALL | re.IGNORECASE)
                body = re.sub(r'<script[^>]*>.*?</script>', '', body, flags=re.DOTALL | re.IGNORECASE)
                body = re.sub(r'<[^>]+>', '', body)
                body = re.sub(r'&nbsp;', ' ', body)
                body = re.sub(r'&[a-zA-Z]+;', '', body)
        except Exception:
            pass
    # Normalise les espaces et sauts de ligne
    body = re.sub(r'[ \t]+', ' ', body)
    body = re.sub(r'\n{3,}', '\n\n', body)
    return body.strip()


def sanitize_text_for_llm(text):
    if not text:
        return ""
    # Strip non-ASCII to prevent tokenization issues in small local models
    text = text.encode('ascii', errors='ignore').decode('ascii')
    # Replace double quotes with single quotes to prevent JSON formatting errors
    text = text.replace('"', "'")
    # Replace braces/brackets with parentheses to avoid confusing JSON parsers
    text = text.replace('{', '(').replace('}', ')').replace('[', '(').replace(']', ')')
    # Normalize whitespaces
    text = re.sub(r'\s+', ' ', text)
    # Strip HTML entities
    text = re.sub(r'&[a-zA-Z0-9#]+;', ' ', text)
    return text.strip()



# ─────────────────────────────────────────────────────────────────────────────
# Accès aux mails par catégorie Gmail
# ─────────────────────────────────────────────────────────────────────────────

def search_by_gmail_category(mail, category_query, only_unread=True, count=10):
    """
    Cherche des mails dans une catégorie Gmail spécifique via X-GM-RAW.
    category_query : ex. 'category:primary', 'category:promotions', etc.
    Retourne une liste de UIDs (bytes) les plus récents en premier.

    Note : imaplib exige que la valeur de X-GM-RAW soit passée comme une
    chaîne entre guillemets (quoted string) directement dans les critères.
    On formate donc la requête manuellement sous forme de bytes.
    """
    read_filter = "is:unread " if only_unread else ""
    raw_query = f'{read_filter}{category_query}'
    try:
        # Encode la requête sous forme de critère IMAP littéral quoté
        encoded_query = f'"{raw_query}"'.encode('utf-8')
        status, response = mail.uid('search', None, b'X-GM-RAW', encoded_query)
        if status != "OK" or not response[0]:
            return []
        uids = response[0].split()
        return list(reversed(uids[-count:]))  # les plus récents en premier
    except Exception as e:
        print(f"Avertissement recherche catégorie '{category_query}': {e}", file=sys.stderr)
        return []


def get_unread_counts_by_category(mail):
    """
    Retourne un dict {nom_catégorie: nombre_non_lus} pour chaque onglet Gmail.
    """
    counts = {}
    for label, gm_query in GMAIL_CATEGORIES.items():
        uids = search_by_gmail_category(mail, gm_query, only_unread=True, count=500)
        counts[label.capitalize()] = len(uids)
    return counts


def fetch_email_meta(mail, msg_uid):
    """Récupère expéditeur, sujet, date et extrait du corps d'un mail via son UID."""
    uid_bytes = msg_uid if isinstance(msg_uid, bytes) else msg_uid.encode()
    status, msg_data = mail.uid('fetch', uid_bytes, "(BODY.PEEK[])")
    if status != "OK" or not msg_data or not msg_data[0]:
        return None
    msg = email.message_from_bytes(msg_data[0][1])
    body = get_email_body(msg)
    uid_str = uid_bytes.decode('utf-8') if isinstance(uid_bytes, bytes) else str(uid_bytes)
    return {
        "uid_bytes": uid_bytes,
        "id":        uid_str,
        "from":      decode_mime_header(msg.get("Subject") or ""),
        "sender":    decode_mime_header(msg.get("From") or ""),
        "subject":   decode_mime_header(msg.get("Subject") or ""),
        "date":      msg.get("Date", ""),
        "snippet":   (body[:300] + "...") if len(body) > 300 else body,
    }


# ─────────────────────────────────────────────────────────────────────────────
# Archivage
# ─────────────────────────────────────────────────────────────────────────────

def find_all_mail_folder(mail):
    try:
        status, folders = mail.list()
        if status != "OK":
            return ""
        for folder in folders:
            folder_str = folder.decode('utf-8', errors='ignore')
            if '\\all' in folder_str.lower():
                parts = folder_str.split(' "/" ')
                if len(parts) >= 2:
                    return parts[-1].strip('"')
                parts_alt = folder_str.split(' "/"')
                if len(parts_alt) >= 2:
                    return parts_alt[-1].strip().strip('"')
        for folder in folders:
            folder_str = folder.decode('utf-8', errors='ignore')
            for fallback in ["[Gmail]/Tous les messages", "[Gmail]/All Mail", "Tous les messages", "All Mail"]:
                if fallback.lower() in folder_str.lower():
                    parts = folder_str.split(' "/" ')
                    if len(parts) >= 2:
                        return parts[-1].strip('"')
    except Exception as e:
        print(f"Avertissement dossier d'archivage : {e}", file=sys.stderr)
    return ""


def archive_email(mail, msg_uid, all_mail_folder):
    uid_str = msg_uid.decode('utf-8') if isinstance(msg_uid, bytes) else str(msg_uid)
    if all_mail_folder:
        try:
            quoted_folder = all_mail_folder
            if ' ' in all_mail_folder and not all_mail_folder.startswith('"'):
                quoted_folder = f'"{all_mail_folder}"'
            mail.uid('COPY', uid_str, quoted_folder)
        except Exception as e:
            print(f"Note : Copie impossible : {e}", file=sys.stderr)
    mail.uid('STORE', uid_str, '+FLAGS', '\\Deleted')
    mail.expunge()


# ─────────────────────────────────────────────────────────────────────────────
# Appel LLM
# ─────────────────────────────────────────────────────────────────────────────

def query_llm(system_prompt, user_prompt, settings):
    mode = settings.get("active_mode", "local")

    def try_request(url, model, key):
        headers = {"Content-Type": "application/json"}
        if key:
            headers["Authorization"] = f"Bearer {key}"
        payload = {
            "model": model,
            "messages": [
                {"role": "system", "content": system_prompt},
                {"role": "user",   "content": user_prompt},
            ],
            "temperature": 0.0,
            "max_tokens": 2048,
            "response_format": {"type": "json_object"}
        }

        max_retries = 3
        delay = 2
        for attempt in range(max_retries):
            try:
                req = urllib.request.Request(
                    url,
                    data=json.dumps(payload).encode("utf-8"),
                    headers=headers,
                    method="POST",
                )
                with urllib.request.urlopen(req, timeout=180) as response:
                    res_data = json.loads(response.read().decode("utf-8"))
                    return res_data["choices"][0]["message"]["content"]
            except urllib.error.HTTPError as e:
                if e.code in (429, 503) and attempt < max_retries - 1:
                    retry_after = e.headers.get("Retry-After")
                    sleep_time = delay
                    if retry_after:
                        try:
                            sleep_time = int(retry_after)
                        except ValueError:
                            pass
                    else:
                        sleep_time = delay * (2 ** attempt)
                    print(f"Avertissement LLM ({model}): HTTP {e.code}. Nouvelle tentative dans {sleep_time}s (essai {attempt+1}/{max_retries})...", file=sys.stderr)
                    time.sleep(sleep_time)
                else:
                    print(f"Erreur d'appel LLM ({model}) : {e}", file=sys.stderr)
                    try:
                        error_body = e.read().decode("utf-8")
                        print(f"Détails de l'erreur : {error_body}", file=sys.stderr)
                    except Exception:
                        pass
                    return None
            except Exception as e:
                print(f"Erreur d'appel LLM ({model}) : {e}", file=sys.stderr)
                return None
        return None

    # First try the configured mode
    if mode == "cloud":
        url = settings.get("cloud_base_url", "")
        model = settings.get("cloud_model", "")
        key = settings.get("cloud_api_key", "")
    else:
        url = settings.get("local_base_url", "")
        model = settings.get("local_model", "")
        key = ""

    url_full = url.rstrip("/") + "/chat/completions"
    res = try_request(url_full, model, key)
    if res is not None:
        return res

    # Fallback to local if cloud failed and we were in cloud mode
    if mode == "cloud":
        # Mutatively set active_mode to local for subsequent calls
        settings["active_mode"] = "local"
        local_url = settings.get("local_base_url", "")
        local_model = settings.get("local_model", "")
        if local_url and local_model:
            print("Avertissement: Repli sur le modèle local suite à l'échec du service Cloud...", file=sys.stderr)
            url_full = local_url.rstrip("/") + "/chat/completions"
            res = try_request(url_full, local_model, "")
            if res is not None:
                return res

    return ""


def close_json(s):
    """Ferme les accolades et crochets d'une chaîne JSON tronquée."""
    stack = []
    in_string = False
    escape = False
    for c in s:
        if escape:
            escape = False
            continue
        if c == '\\':
            escape = True
            continue
        if c == '"':
            in_string = not in_string
            continue
        if not in_string:
            if c in '{[':
                stack.append(c)
            elif c in '}]':
                if stack:
                    stack.pop()
    if in_string:
        s += '"'
    while stack:
        opened = stack.pop()
        if opened == '{':
            s += '}'
        elif opened == '[':
            s += ']'
    return s


def parse_llm_json(raw):
    """
    Extrait et parse un objet JSON de la réponse LLM.
    Gère les blocs de réflexion (<think>...</think>), les blocs markdown (```json),
    et les textes superflus autour du JSON.
    """
    if not raw:
        return {}
        
    raw = raw.strip()
    
    # 1. Supprime les blocs de réflexion <think>...</think> si présents
    raw = re.sub(r'<think>.*?</think>', '', raw, flags=re.DOTALL | re.IGNORECASE)
    raw = raw.strip()
    
    # 2. Cherche un bloc de code JSON markdown ```json ... ``` ou ``` ... ```
    json_block_match = re.search(r'```(?:json)?\s*(\{.*?\})\s*```', raw, flags=re.DOTALL)
    if json_block_match:
        try:
            return json.loads(json_block_match.group(1).strip())
        except Exception:
            pass
            
    # 3. Fallback : cherche le premier '{' et le dernier '}'
    start = raw.find('{')
    end = raw.rfind('}')
    if start != -1 and end != -1 and end > start:
        candidate = raw[start:end+1].strip()
        try:
            return json.loads(candidate)
        except Exception as e:
            # Essaye de fermer le JSON si tronqué
            try:
                closed = close_json(candidate)
                return json.loads(closed)
            except Exception:
                pass
            # Nettoie les retours à la ligne ou guillemets échappés invalides si possible
            candidate_cleaned = candidate.replace(r"\'", "'").replace(r'\"', '"')
            try:
                return json.loads(candidate_cleaned)
            except Exception:
                raise e
                
    # 4. Essaye de parser le texte brut en dernier recours
    try:
        return json.loads(raw)
    except Exception:
        try:
            return json.loads(close_json(raw))
        except Exception:
            raise


# ─────────────────────────────────────────────────────────────────────────────
# Main
# ─────────────────────────────────────────────────────────────────────────────

def main():
    config_path = "../../pixel_core_memory.json"
    if not os.path.exists(config_path):
        print("Erreur : Fichier pixel_core_memory.json introuvable.")
        sys.exit(1)

    try:
        with open(config_path, "r", encoding="utf-8") as f:
            config = json.load(f)
    except Exception as e:
        print(f"Erreur de lecture de la configuration : {e}")
        sys.exit(1)

    settings     = config.get("gmail_settings", {})
    email_addr   = settings.get("email", "")
    password     = settings.get("app_password", "")
    llm_settings = config.get("llm_settings", {})

    if not email_addr or not password:
        print("La configuration Gmail n'est pas encore complétée ou activée dans tes paramètres.")
        sys.exit(0)

    query = ""
    if len(sys.argv) > 1:
        query = sys.argv[1].strip()

    try:
        mail = imaplib.IMAP4_SSL("imap.gmail.com")
        mail.login(email_addr, password)
        mail.select("inbox")
        all_mail_folder = find_all_mail_folder(mail)

        # ── MARQUER COMME LUS ──────────────────────────────────────────────
        if query == "mark_as_read":
            print("Marquage des e-mails non lus comme vus...")
            status, response = mail.uid('search', None, "UNSEEN")
            if status != "OK" or not response[0]:
                print("Tu n'as aucun e-mail non lu à marquer comme lu.")
                sys.exit(0)
            msg_ids = response[0].split()
            for msg_id in msg_ids:
                mail.uid('STORE', msg_id, '+FLAGS', '\\Seen')
            print(f"J'ai marqué {len(msg_ids)} e-mail(s) comme lu(s) avec succès.")

        # ── NETTOYAGE INTELLIGENT ──────────────────────────────────────────
        elif query == "clean_inbox":
            print("Mise en ordre de la boîte de réception en cours...")
            status, response = mail.uid('search', None, "ALL")
            if status != "OK" or not response[0]:
                print("Aucun e-mail à trier.")
                sys.exit(0)

            uids = response[0].split()
            if not uids:
                print("Aucun e-mail à trier.")
                sys.exit(0)

            recent_uids = uids[-15:]
            emails_to_classify = []
            uid_map = {}

            for msg_uid in reversed(recent_uids):
                meta = fetch_email_meta(mail, msg_uid)
                if not meta:
                    continue
                emails_to_classify.append({
                    "id":      meta["id"],
                    "from":    meta["sender"],
                    "subject": meta["subject"],
                    "snippet": meta["snippet"],
                })
                uid_map[meta["id"]] = meta

            if not emails_to_classify:
                print("Aucun e-mail à trier.")
                sys.exit(0)

            system_prompt = (
                "Tu es un assistant de tri d'e-mails. Analyse la liste d'e-mails et décide pour chacun "
                "s'il s'agit d'un \"clutter\" (publicité, newsletter, notification automatique, reçu d'achat, spam) "
                "qui doit être ARCHIVÉ directement, ou s'il s'agit d'un e-mail IMPORTANT ou de correspondance active "
                "(message personnel, professionnel, urgence, question directe, info critique) qui doit rester dans la boîte.\n\n"
                "Réponds UNIQUEMENT sous forme d'un objet JSON (sans blocs markdown) :\n"
                "{\n"
                "  \"to_archive\": [\"id_1\", \"id_2\"],\n"
                "  \"reasons\": {\n"
                "    \"id_1\": \"Raison courte (ex: Publicité / Newsletter)\"\n"
                "  }\n"
                "}"
            )
            llm_raw = query_llm(system_prompt, json.dumps(emails_to_classify, indent=2), llm_settings)

            try:
                decision = parse_llm_json(llm_raw)
            except Exception as e:
                print(f"Erreur de tri : Impossible de parser la décision de l'IA. ({e})")
                sys.exit(1)

            to_archive_ids = decision.get("to_archive", [])
            reasons        = decision.get("reasons", {})
            archived_count = kept_count = 0

            print("─── NETTOYAGE INTELLIGENT DE LA BOÎTE DE RÉCEPTION ───")
            for e_info in emails_to_classify:
                e_id = e_info["id"]
                meta = uid_map[e_id]
                if e_id in to_archive_ids:
                    reason = reasons.get(e_id, "Non-important")
                    archive_email(mail, meta["uid_bytes"], all_mail_folder)
                    print(f"📦 ARCHIVÉ : '{meta['subject']}' de {meta['sender']} → ({reason})")
                    archived_count += 1
                else:
                    print(f"📥 CONSERVÉ : '{meta['subject']}' de {meta['sender']}")
                    kept_count += 1

            print(f"\nBilan : {archived_count} mail(s) archivé(s), {kept_count} mail(s) conservé(s).")

        # ── ARCHIVER UN MAIL SPÉCIFIQUE ────────────────────────────────────
        elif query.startswith("archive:"):
            target = query[8:].strip()
            if not target:
                print("Spécifie un index ou un expéditeur à archiver.")
                sys.exit(1)

            if target.isdigit():
                idx = int(target) - 1
                # On cherche dans l'onglet Principal d'abord
                uids = search_by_gmail_category(mail, "category:primary", only_unread=True, count=20)
                if not uids:
                    status, response = mail.uid('search', None, "UNSEEN")
                    uids = list(reversed(response[0].split())) if status == "OK" and response[0] else []
                if 0 <= idx < len(uids):
                    target_uid = uids[idx]
                    meta = fetch_email_meta(mail, target_uid)
                    if meta:
                        archive_email(mail, target_uid, all_mail_folder)
                        print(f"L'e-mail [{idx+1}] a été archivé avec succès : '{meta['subject']}' de {meta['sender']}.")
                        sys.exit(0)
                print(f"Impossible de trouver l'e-mail non lu à l'index {target}.")
            else:
                status, response = mail.uid('search', 'X-GM-RAW', f'"{target}"')
                if status == "OK" and response[0]:
                    uids = response[0].split()
                    if uids:
                        target_uid = uids[-1]
                        meta = fetch_email_meta(mail, target_uid)
                        if meta:
                            archive_email(mail, target_uid, all_mail_folder)
                            print(f"L'e-mail a été archivé avec succès : '{meta['subject']}' de {meta['sender']}.")
                            sys.exit(0)
                print(f"Aucun e-mail correspondant à '{target}' n'a été trouvé pour archivage.")

        # ── CATÉGORISATION INTELLIGENTE ────────────────────────────────────
        elif query == "categorize":
            print("📊 Analyse de ta boîte mail par onglet Gmail en cours...\n")
            print("LECTURE SEULE - aucun mail ne sera archive ni supprime.\n")

            # Lit jusqu'a 5 mails non lus PAR ONGLET Gmail
            onglets = [
                ("Principal",       "category:primary"),
                ("Reseaux sociaux", "category:social"),
                ("Notifications",   "category:updates"),
                ("Promotions",      "category:promotions"),
            ]

            emails_to_classify = []
            uid_map = {}
            onglet_map = {}

            for onglet_name, gm_query in onglets:
                uids = search_by_gmail_category(mail, gm_query, only_unread=True, count=5)
                for msg_uid in uids:
                    meta = fetch_email_meta(mail, msg_uid)
                    if not meta or meta["id"] in uid_map:
                        continue
                    emails_to_classify.append({
                        "id":      meta["id"],
                        "onglet":  onglet_name,
                        "from":    meta["sender"],
                        "subject": meta["subject"],
                        "snippet": meta["snippet"][:200],
                    })
                    uid_map[meta["id"]]    = meta
                    onglet_map[meta["id"]] = onglet_name

            if not emails_to_classify:
                print("Aucun e-mail non lu trouve dans tes onglets Gmail.")
                sys.exit(0)

            print(f"Emails non lus analyses : {len(emails_to_classify)} (max 5 par onglet).\n")

            system_prompt = (
                "Tu es un assistant expert en gestion de boite mail. "
                "Classe chaque e-mail dans UNE SEULE categorie :\n"
                "- Important : vrai message personnel ou professionnel d'une vraie personne, urgence, question directe\n"
                "- Informations : newsletters d'actualites, journaux (TF1, Le Monde, etc.)\n"
                "- Reseaux sociaux : notifications LinkedIn, Twitter/X, Facebook, Instagram, etc.\n"
                "- Promotions : publicites, offres commerciales, soldes, codes promo\n"
                "- Notifications : confirmations, alertes automatiques de services, 2FA, factures\n"
                "- Autre : ne rentre dans aucune autre categorie\n\n"
                "Pour les mails 'Important' mets alert=true, pour tous les autres alert=false.\n"
                "Ne dis jamais que tu vas archiver ou supprimer des mails.\n\n"
                "Reponds UNIQUEMENT en JSON valide sans blocs markdown :\n"
                '{"emails": [{"id": "...", "category": "Important", "alert": true, "summary": "Resume 1 phrase"}, ...]}'
            )

            llm_raw = query_llm(system_prompt, json.dumps(emails_to_classify, indent=2), llm_settings)

            try:
                result = parse_llm_json(llm_raw)
            except Exception as e:
                print(f"Erreur de catégorisation : Impossible de parser la réponse de l'IA. ({e})")
                sys.exit(1)

            classified = result.get("emails", [])

            # Regrouper par categorie
            by_category = {}
            alerts = []
            for item in classified:
                cat     = item.get("category", "Autre")
                e_id    = item.get("id", "")
                summary = item.get("summary", "")
                alert   = item.get("alert", False)
                meta    = uid_map.get(e_id, {})
                onglet  = onglet_map.get(e_id, "?")

                if cat not in by_category:
                    by_category[cat] = []
                by_category[cat].append({"meta": meta, "summary": summary, "onglet": onglet})

                if alert:
                    alerts.append({"meta": meta, "summary": summary, "onglet": onglet})

            # Alertes en priorite
            if alerts:
                print("MAILS IMPORTANTS A LIRE EN PRIORITE :")
                for a in alerts:
                    m = a["meta"]
                    print(f"  [IMPORTANT][{a['onglet']}] De : {m.get('sender', '?')}")
                    print(f"  Sujet : {m.get('subject', '?')}")
                    print(f"  -> {a['summary']}")
                    print()
            else:
                print("Aucun mail urgent ou personnel detecte.\n")

            # Affichage par categorie
            category_order = ["Important", "Informations", "Reseaux sociaux", "Promotions", "Notifications", "Autre"]
            cat_labels = {
                "Important":      "[IMPORTANT]",
                "Informations":   "[INFO]",
                "Reseaux sociaux":"[SOCIAL]",
                "Promotions":     "[PROMO]",
                "Notifications":  "[NOTIF]",
                "Autre":          "[AUTRE]",
            }

            for cat in category_order:
                items = by_category.get(cat, [])
                if not items:
                    continue
                label = cat_labels.get(cat, "[?]")
                print(f"{label} {cat.upper()} : {len(items)} mail(s)")
                for item in items:
                    m = item["meta"]
                    print(f"  [{item['onglet']}] De : {m.get('sender', '?')}")
                    print(f"  Sujet : {m.get('subject', '?')}")
                    print(f"  -> {item['summary']}")
                    print()

            total = len(classified)
            important_count = len(by_category.get("Important", []))
            print("-" * 50)
            print(f"Bilan : {total} mail(s) analyses - {important_count} important(s) - AUCUN archivage effectue.")

        # ── TRI PHYSIQUE PAR CATÉGORIE GMAIL ──────────────────────────────
        elif query == "sort_inbox":
            print("Tri de ta boite mail en cours...")
            print("Les mails Promotions, Reseaux sociaux et Notifications seront deplaces vers leurs onglets respectifs.")
            print("Seuls tes mails personnels importants resteront dans l'onglet Principal.\n")

            # Correspondance categorie LLM -> label Gmail IMAP
            CATEGORY_TO_LABEL = {
                "Promotions":      "CATEGORY_PROMOTIONS",
                "Reseaux sociaux": "CATEGORY_SOCIAL",
                "Notifications":   "CATEGORY_UPDATES",
                "Informations":    "CATEGORY_UPDATES",
                "Autre":           "CATEGORY_UPDATES",
                # "Important" reste dans INBOX, pas de label supplementaire
            }

            # Recupere les mails non lus de l'onglet Principal uniquement
            primary_uids = search_by_gmail_category(mail, "category:primary", only_unread=True, count=30)
            if not primary_uids:
                print("Aucun mail a trier dans l'onglet Principal.")
                sys.exit(0)

            print(f"{len(primary_uids)} mail(s) trouves dans l'onglet Principal a analyser...\n")

            classified = []
            uid_map = {}

            system_prompt = (
                "Tu es un assistant de tri de boite mail. Donne la catégorie de l'e-mail fourni par l'utilisateur parmi les suivantes :\n"
                "- Important (message personnel ou professionnel direct d'une vraie personne, urgence)\n"
                "- Informations (newsletters d'actualités, articles, journaux, blogs)\n"
                "- Reseaux sociaux (notifications LinkedIn, Facebook, Twitter, etc.)\n"
                "- Promotions (publicités, offres commerciales, soldes)\n"
                "- Notifications (confirmations de commande, factures, alertes automatiques, 2FA, services)\n"
                "- Autre (tout ce qui ne rentre pas dans les autres catégories)\n\n"
                "Réponds sous forme d'un objet JSON au format exact :\n"
                "{\"category\": \"NomDeLaCategorie\"}"
            )

            for idx, msg_uid in enumerate(primary_uids, 1):
                meta = fetch_email_meta(mail, msg_uid)
                if not meta:
                    continue

                uid_map[meta["id"]] = meta

                sender = sanitize_text_for_llm(meta["sender"])
                subject = sanitize_text_for_llm(meta["subject"])
                snippet = sanitize_text_for_llm(meta["snippet"][:150])

                user_prompt = f"De : {sender}\nSujet : {subject}\nExtrait : {snippet}"

                print(f"Analyse du mail {idx}/{len(primary_uids)} : '{meta['subject'][:50]}'...")
                llm_raw = query_llm(system_prompt, user_prompt, llm_settings)

                cat = "Autre"
                try:
                    result = parse_llm_json(llm_raw)
                    cat = result.get("category", "Autre")
                except Exception:
                    # Simple fallback to substring search if JSON parsing fails
                    for candidate_cat in ["Important", "Informations", "Reseaux sociaux", "Promotions", "Notifications", "Autre"]:
                        if candidate_cat.lower() in llm_raw.lower():
                            cat = candidate_cat
                            break

                # Normalise la catégorie pour correspondre aux clés
                normalized_cat = "Autre"
                for candidate_cat in ["Important", "Informations", "Reseaux sociaux", "Promotions", "Notifications", "Autre"]:
                    if cat.strip().lower() == candidate_cat.lower():
                        normalized_cat = candidate_cat
                        break

                classified.append({
                    "id": meta["id"],
                    "category": normalized_cat
                })

            moved_counts = {}
            kept_count = 0
            errors = 0

            print("\nTri en cours...\n")
            for item in classified:
                e_id    = item.get("id", "")
                cat     = item.get("category", "Autre")
                meta    = uid_map.get(e_id)
                if not meta:
                    continue

                label = CATEGORY_TO_LABEL.get(cat)
                uid_str = meta["id"]

                if label is None:
                    # Important -> reste dans Principal, rien a faire
                    print(f"  [GARDE] [{cat}] {meta['subject'][:60]}")
                    kept_count += 1
                else:
                    # Deplacer vers le bon onglet en ajoutant le label Gmail
                    try:
                        # Ajoute le label de categorie (deplace vers l'onglet)
                        mail.uid('STORE', uid_str, '+X-GM-LABELS', label)
                        # Retire le label INBOX pour sortir de Principal
                        mail.uid('STORE', uid_str, '-X-GM-LABELS', '\\Inbox')
                        print(f"  [DEPLACE -> {cat}] {meta['subject'][:60]}")
                        moved_counts[cat] = moved_counts.get(cat, 0) + 1
                    except Exception as e:
                        print(f"  [ERREUR] {meta['subject'][:50]}: {e}")
                        errors += 1

            print("\n" + "-" * 50)
            print(f"Tri termine !")
            print(f"  Gardes dans Principal (importants) : {kept_count}")
            for cat, count in moved_counts.items():
                print(f"  Deplaces vers {cat} : {count}")
            if errors:
                print(f"  Erreurs : {errors}")

        # ── RAPPORT COMPLET PAR ONGLET ─────────────────────────────────────
        elif query == "inbox_report":

            print("📊 Rapport de ta boîte Gmail par onglet...\n")
            counts = get_unread_counts_by_category(mail)

            total = sum(counts.values())
            if total == 0:
                print("🎉 Ta boîte mail est entièrement à jour ! Aucun message non lu.")
                sys.exit(0)

            print(f"Tu as {total} message(s) non lu(s) au total :\n")
            for cat_name, count in counts.items():
                emoji = CATEGORY_EMOJI.get(cat_name, "📧")
                bar = "█" * min(count, 20)
                print(f"  {emoji} {cat_name:<20} : {count:>4} non lu(s)  {bar}")

            # Affiche un aperçu des mails importants de l'onglet Principal
            primary_uids = search_by_gmail_category(mail, "category:primary", only_unread=True, count=5)
            if primary_uids:
                print(f"\n📬 ─── Aperçu des mails non lus dans l'onglet Principal ──────")
                for i, uid in enumerate(primary_uids, 1):
                    meta = fetch_email_meta(mail, uid)
                    if meta:
                        print(f"  [{i}] De : {meta['sender']}")
                        print(f"      Sujet : {meta['subject']}")
                        print(f"      Extrait : {meta['snippet'][:150]}...")
                        print()

        # ── RECHERCHE PAR MOT-CLÉ ─────────────────────────────────────────
        elif query:
            print(f"Recherche d'e-mails contenant '{query}'...")
            status, response = mail.uid('search', 'X-GM-RAW', f'"{query}"')
            if status != "OK" or not response[0]:
                print(f"Aucun e-mail trouvé correspondant à la recherche '{query}'.")
                sys.exit(0)
            msg_ids = list(reversed(response[0].split()[-10:]))

            found_emails = []
            for msg_id in msg_ids:
                meta = fetch_email_meta(mail, msg_id)
                if meta:
                    found_emails.append(meta)

            if not found_emails:
                print(f"Aucun e-mail trouvé correspondant à la recherche '{query}'.")
            else:
                print(f"Voici les e-mails correspondants ({len(found_emails)}) :")
                for i, m in enumerate(found_emails, 1):
                    print(f"\n[{i}] De : {m['sender']}\n    Sujet : {m['subject']}\n    Date : {m['date']}\n    Extrait : {m['snippet']}")

        # ── VÉRIFICATION PAR DÉFAUT — PRIORITÉ ONGLET PRINCIPAL ───────────
        else:
            print("Vérification des nouveaux e-mails dans l'onglet Principal...\n")

            # 1. Cherche d'abord dans l'onglet Principal
            primary_uids = search_by_gmail_category(mail, "category:primary", only_unread=True, count=5)

            if not primary_uids:
                # 2. Fallback : cherche dans toute la boîte
                print("Aucun nouveau mail dans l'onglet Principal.")
                status, response = mail.uid('search', None, "UNSEEN")
                if status != "OK" or not response[0]:
                    print("Tu n'as aucun nouvel e-mail non lu.")
                    sys.exit(0)
                all_uids = list(reversed(response[0].split()[-5:]))
                if not all_uids:
                    print("Tu n'as aucun nouvel e-mail non lu.")
                    sys.exit(0)
                # Compte par catégorie pour informer
                counts = get_unread_counts_by_category(mail)
                total = sum(counts.values())
                print(f"Tu as cependant {total} mail(s) non lu(s) en tout :")
                for cat_name, count in counts.items():
                    if count > 0:
                        emoji = CATEGORY_EMOJI.get(cat_name, "📧")
                        print(f"  {emoji} {cat_name} : {count} non lu(s)")
                print("\nDis-moi 'rapport boîte mail' pour voir le détail, ou 'catégoriser mes mails' pour un tri complet.")
                sys.exit(0)

            # 3. Affiche les mails de l'onglet Principal
            print(f"Tu as {len(primary_uids)} nouveau(x) e-mail(s) dans ton onglet Principal :\n")

            important_mails = []
            for i, uid in enumerate(primary_uids, 1):
                meta = fetch_email_meta(mail, uid)
                if not meta:
                    continue
                print(f"[{i}] De : {meta['sender']}")
                print(f"    Sujet : {meta['subject']}")
                print(f"    Date  : {meta['date']}")
                print(f"    Extrait : {meta['snippet'][:250]}{'...' if len(meta['snippet']) > 250 else ''}")
                print()
                important_mails.append(meta)

            # 4. Avertit sur les autres catégories
            counts = get_unread_counts_by_category(mail)
            other_total = sum(v for k, v in counts.items() if k != "Principal")
            if other_total > 0:
                print(f"──────────────────────────────────────────────")
                print(f"ℹ️  Tu as aussi {other_total} mail(s) non lus dans les autres onglets :")
                for cat_name, count in counts.items():
                    if cat_name != "Principal" and count > 0:
                        emoji = CATEGORY_EMOJI.get(cat_name, "📧")
                        print(f"  {emoji} {cat_name} : {count}")
                print("Dis-moi 'rapport boîte mail' pour le détail ou 'catégoriser mes mails' pour un tri intelligent.")

        mail.logout()

    except Exception as e:
        print(f"Erreur d'accès à Gmail : {e}")


if __name__ == '__main__':
    main()
