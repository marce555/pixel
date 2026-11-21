---
name: openaudio_dj_mix
description: Moteur DJ OpenAudio (librosa, Mixxx DB, FFmpeg Rubberband, SciPy/NumPy DSP) avec normalisation EBU R128 (-14 LUFS), effets 80s (jet_flanger, echo_out, filter_sweep, vinyl_brake), sélection harmonique Camelot, courbe d'énergie Storytelling et mode radio Concierto Discoteque.
---

# OpenAudio Pro DJ Mix Skill

## Architecture & Non-Blocking Design
- **`skills/openaudio_dj_mix/manifest.json`** : Déclaration chargée par `internal/skills/manager.go`.
- **`skills/openaudio_dj_mix/run.py`** : Point d'entrée rapide. Décode uniquement la piste #1 (`part_000`) en solo depuis le début réel (`find_leading_silence_sample`), écrit `plan.json` et lance le worker détaché `openaudio_mix_worker.py` en arrière-plan pour ne **jamais bloquer** le thread principal Go.
- **`skills/openaudio_dj_mix/openaudio_mix_worker.py`** : Génère progressivement `part_001 .. part_N` en avance sur la lecture (`mpv` IPC gapless sur PC ou `--batch` + `/api/media/mix` sur Android).
- **`skills/openaudio_dj_mix/openaudio_engine.py`** : Cerveau d'analyse (BPM, Camelot Wheel, HPSS intro/outro, cache global + cache JSON local par répertoire dans `~/.cache/pixel_openaudio_dj/folders/`) et moteur DSP (`Rubberband`, `soxr`, `EBU R128 LUFS`, transitions & effets 80s).
- **Routage Go** : `internal/agent/superior.go` (`detectOpenAudioMixIntent` / `handleOpenAudioMixDirect`) et `internal/scheduler/handlers.go`.

---

## Paramètres CLI & JSON Exposés

Le script `run.py` accepte les requêtes naturelles (`"lance un mix avec Hits-Remix"`, `"lance Concierto Discoteque Hits-Remix en commençant par ..."`) ainsi que des paramètres explicites en CLI ou en objet JSON :

| Paramètre | Valeurs possibles | Défaut | Description |
| :--- | :--- | :--- | :--- |
| `transition_type` (`--transition-type`) | `auto`, `drop_in`, `crossfade` | `auto` | `drop_in` (entrée 80%→100%, maintien 5 s + fondu progressif), `crossfade` (Bass-Swap ~10–12 s beat-locked), ou `auto` (alternance dynamique + repli `crossfade` si rythme faible). |
| `effect` (`--effect`) | `auto`, `none`, `jet_flanger`, `echo_out`, `filter_sweep`, `vinyl_brake` | `auto` | Effet DSP appliqué sur la piste sortante / montée de transition (`auto` sélectionne dynamiquement l'effet adapté au passage). |
| `mix_in_sec` (`--mix-in-sec`) | `float` (secondes) | `None` (HPSS auto) | Point d'entrée forcé (en secondes) sur les pistes entrantes `1..N`. |
| `mix_out_sec` (`--mix-out-sec`) | `float` (secondes) | `None` (HPSS auto) | Point de sortie forcé (en secondes) avant la transition. |
| `folder` | `string` | `""` | Nom du répertoire cible dans `~/Musique`. |
| `start_track` | `string` | `""` | Titre/fichier exact ou partiel pour imposer la piste #1. |
| `--render-transition-test` | `<trackA> <trackB> --out <file>` | — | Mode validation CLI isolé rendant uniquement une transition test (`5 s` avant + transition + `6 s` après). |

### Exemple d'appel JSON structuré
```bash
skills/openaudio_dj_mix/venv/bin/python skills/openaudio_dj_mix/run.py \
  '{"folder":"Hits-Remix","start_track":"Celebration","transition_type":"drop_in","effect":"jet_flanger"}'
```

---

## Moteur d'Effets Audio DSP (Années 80)

1. **`jet_flanger` (`apply_jet_flanger`)** : Flanger/comb LFO modulé entre `0,5 ms` et `4,3 ms` avec feedback élevé (`0.65`) synchronisé au tempo sur les montées instrumentales et avant les Drop-In.
2. **`echo_out` (`apply_echo_out`)** : Calcul dynamique du délai selon le BPM sortant (`delay_ms = 60000 / bpm` à la noire, `30000 / bpm` à la croche par défaut), appliqué sur les `2` à `4` derniers temps avant coupure avec réchauffement passe-bas BBD (`3,2 kHz`).
3. **`filter_sweep` (`apply_filter_sweep`)** : Balayage progressif de fréquence passe-haut (`70 Hz → 2200 Hz`, ou passe-bas `lowpass`) multi-bandes sans clic pour dégager les basses pendant l'entrée de la piste suivante.
4. **`vinyl_brake` (`apply_vinyl_brake`)** : Ralentissement rapide du pitch/tempo (`~0,65 s`) simulant l'arrêt d'une platine vinyle avant un Drop-In percutant.

---

## Règles de Direction Artistique DJ ("Creative Set Builder")

1. **Règle Harmonique (Camelot Wheel)** :
   - Privilégier strictement les transitions compatibles : même clé (`8A → 8A`, score +30), déplacement `±1` sur la roue (`8A → 9A` / `7A`, +25) ou passage relatif Majeur/Mineur (`8A ↔ 8B`, +22).
2. **Interdiction de Superposer Deux Voix** :
   - Le détecteur HPSS (`find_mix_windows_from_pcm`) sépare harmoniques vocales (`300–3200 Hz`) et percussions/kicks (`40–200 Hz`) pour placer `mix_in_sec` sur une zone batterie sans chant et `mix_out_sec` sur un break/outro instrumental.
   - En mode **Concierto Discoteque**, les singels (`~/Musique/Marcas/singels/` ou `~/Musique/Marcas/`) et l'Apertura sont placés après `4–5 s` sur la zone la moins vocale (`find_best_jingle_spot_in_body`) avec la musique maintenue à `100 %` (sans baisse de volume).
3. **Gestion de la Courbe BPM & Énergie (Storytelling) & Durée Adaptative de Mix** :
   - **Durée adaptative automatique (`assess_adaptive_transition_pace`)** :
     - **`long` (`12–21 s` / `24–40+` temps)** : style *Concierto années 80* lorsque les deux morceaux ont une piste rythmique stable (`CV <= 0.115`) et un long couloir sans conflit vocal (`>= 12,5 s`).
     - **`medium` (`8,2–11,2 s` / `16–20` temps)** : transition équilibrée par défaut (`5 s` de maintien en Drop-In).
     - **`quick` (`4,2–6,2 s` / `8–12` temps)** : passage serré dès que les voix arrivent vite, que la queue est courte ou que le tempo d'origine est instable.
   - Démarrage mid-tempo (`~108–118 BPM` lorsque le répertoire le permet, sauf si un titre de départ est imposé — qui démarre alors depuis le vrai début via `find_leading_silence_sample`).
   - Montée progressive vers le pic d'énergie (`~122–128 BPM`) sur le cœur du set, puis respiration légère sur le final.
   - Stretch **Rubberband Master Tempo** borné à `±8 %` pour conserver le timbre naturel, suivi d'une normalisation **EBU R128 (`-14.0 LUFS`)** sur l'intégralité de chaque piste.
4. **Cache JSON Local par Répertoire** :
   - Chaque répertoire analysé est mis en cache dans `~/.cache/pixel_openaudio_dj/folders/<dossier>_<hash>.json` (BPM, clé Camelot, `mix_in_sec`, `mix_out_sec`, énergie, `vocal_ratio`).

---

## Validation CLI

Tester une transition avec `jet_flanger` ou `echo_out` entre deux pistes locales :
```bash
/home/marceloc/Documents/Pixel/skills/openaudio_dj_mix/venv/bin/python \
  /home/marceloc/Documents/Pixel/skills/openaudio_dj_mix/run.py \
  --render-transition-test "<chemin_piste_A.mp3>" "<chemin_piste_B.mp3>" \
  --transition-type drop_in --effect jet_flanger --out /tmp/test_jet.flac
```
