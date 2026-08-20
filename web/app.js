/* ── Toast notifications système (hors flux chat) ─────────────────────── */
window.showToast = function(message, type = 'info') {
    const toast = document.createElement('div');
    const colors = {
        info:    'rgba(96,165,250,0.15)',
        warning: 'rgba(245,158,11,0.15)',
        error:   'rgba(239,68,68,0.15)',
    };
    const borders = {
        info:    'rgba(96,165,250,0.4)',
        warning: 'rgba(245,158,11,0.4)',
        error:   'rgba(239,68,68,0.4)',
    };
    toast.style.cssText = `
        position: fixed; bottom: 80px; left: 50%; transform: translateX(-50%) translateY(20px);
        background: ${colors[type] || colors.info};
        border: 1px solid ${borders[type] || borders.info};
        backdrop-filter: blur(12px); -webkit-backdrop-filter: blur(12px);
        color: rgba(255,255,255,0.85); padding: 8px 16px; border-radius: 20px;
        font-size: 0.82em; font-family: Inter, sans-serif; font-style: italic;
        max-width: 420px; text-align: center; z-index: 9999;
        opacity: 0; transition: opacity 0.3s ease, transform 0.3s ease;
        pointer-events: none; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
    `;
    toast.textContent = message;
    document.body.appendChild(toast);
    requestAnimationFrame(() => {
        toast.style.opacity = '1';
        toast.style.transform = 'translateX(-50%) translateY(0)';
    });
    setTimeout(() => {
        toast.style.opacity = '0';
        toast.style.transform = 'translateX(-50%) translateY(10px)';
        setTimeout(() => toast.remove(), 400);
    }, 4000);
};

document.addEventListener('DOMContentLoaded', () => {
    const chatBox = document.getElementById('chat-box');
    const chatForm = document.getElementById('chat-form');
    const userInput = document.getElementById('user-input');
    let currentUserName = "Marcelo";
    
    // Charger la Core Memory, les apprentissages, l'historique du chat et la santé matérielle
    fetchCoreMemory();
    fetchLTMLearnings();
    fetchChatHistory();
    fetchSystemStatus();
    fetchActiveProject();
    fetchTasks();
    fetchLLMSettings();

    // Rafraichir toutes les 10 secondes pour voir l'évolution
    setInterval(() => {
        fetchCoreMemory();
        fetchLTMLearnings();
        fetchSystemStatus();
        fetchActiveProject();
    }, 10000);

    // Rafraichir l'état du Scheduler de tâches toutes les 2 secondes (temps réel)
    setInterval(() => {
        fetchTasks();
    }, 2000);

    // Menu déroulant "Cerveau & Mémoire" et gestion des onglets
    const menuBtn = document.getElementById('memory-menu-btn');
    const dropdown = document.getElementById('memory-dropdown');
    
    if (menuBtn && dropdown) {
        menuBtn.addEventListener('click', (e) => {
            e.stopPropagation();
            const isShown = dropdown.classList.toggle('show');
            menuBtn.classList.toggle('active', isShown);
            menuBtn.setAttribute('aria-expanded', isShown ? 'true' : 'false');
        });

        // Fermer le menu lors d'un clic en dehors
        document.addEventListener('click', (e) => {
            if (!dropdown.contains(e.target) && !menuBtn.contains(e.target)) {
                dropdown.classList.remove('show');
                menuBtn.classList.remove('active');
                menuBtn.setAttribute('aria-expanded', 'false');
            }
        });
    }

    // Commutation des onglets
    const tabButtons = document.querySelectorAll('.tab-btn');
    tabButtons.forEach(btn => {
        btn.addEventListener('click', () => {
            const tabId = btn.getAttribute('data-tab');
            switchToTab(tabId);
        });
    });

    function switchToTab(tabId) {
        document.querySelectorAll('.tab-btn').forEach(b => {
            b.classList.toggle('active', b.getAttribute('data-tab') === tabId);
        });
        document.querySelectorAll('.tab-pane').forEach(p => {
            p.classList.toggle('active', p.id === tabId);
        });
    }

    // Écouter les événements proactifs du serveur (CuriosityAgent)
    const eventSource = new EventSource('/api/events');
    eventSource.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);
            if (data.proactive_message) {
                if (data.proactive_message.startsWith("VISION:")) {
                    const desc = data.proactive_message.substring(7);
                    const visionDescEl = document.getElementById('vision-desc');
                    if (visionDescEl) {
                        visionDescEl.textContent = desc;
                        // Sleek fade-in feedback
                        visionDescEl.style.opacity = '0';
                        setTimeout(() => {
                            visionDescEl.style.transition = 'opacity 0.5s ease';
                            visionDescEl.style.opacity = '1';
                        }, 50);
                    }
                } else {
                    appendMessage(data.proactive_message, 'pixel');
                    if (window.voiceController) {
                        window.voiceController.startSpeechSession();
                        window.voiceController.feedChunk(data.proactive_message);
                        window.voiceController.finalizeSpeech();
                    }
                }
            }
        } catch (e) {
            console.error("Error parsing proactive event:", e);
        }
    };

    const sleepBtn = document.getElementById('force-sleep-btn');
    sleepBtn.addEventListener('click', async () => {
        sleepBtn.disabled = true;
        sleepBtn.innerHTML = "Consolidation en cours...";
        try {
            await fetch('/api/force_sleep', { method: 'POST' });
            await fetchCoreMemory();
        } catch (err) {
            console.error(err);
        }
        sleepBtn.disabled = false;
        sleepBtn.innerHTML = `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"></path></svg> Forcer Sommeil (Sauvegarder)`;
    });

    function formatResponse(text) {
        let responseText = text;
        let thoughtText = "";

        const thoughtMarkers = [
            "**Pensée interne de Pixel :**",
            "**Pensée interne de Pixel:**",
            "Pensée interne de Pixel :",
            "Pensée interne de Pixel:",
            "**Pensée interne :**",
            "**Pensée interne:**",
            "Pensée interne :",
            "Pensée interne:"
        ];

        let markerIndex = -1;
        let markerLength = 0;

        for (const marker of thoughtMarkers) {
            const idx = responseText.indexOf(marker);
            if (idx !== -1) {
                markerIndex = idx;
                markerLength = marker.length;
                break;
            }
        }

        if (markerIndex !== -1) {
            thoughtText = responseText.substring(markerIndex + markerLength).trim();
            responseText = responseText.substring(0, markerIndex).trim();

            // Clean up asterisks and underscores around the thought
            thoughtText = thoughtText.replace(/^[\s*_~]+|[\s*_~]+$/g, '');
        }

        const directMarkers = [
            "**Réponse directe :**",
            "**Réponse directe:**",
            "Réponse directe :",
            "Réponse directe:",
            "**Réponse :**",
            "**Réponse:**",
            "Réponse :",
            "Réponse:"
        ];

        for (const marker of directMarkers) {
            if (responseText.startsWith(marker)) {
                responseText = responseText.substring(marker.length).trim();
                break;
            }
        }
        
        responseText = responseText.replace(/^[\s*_~]+|[\s*_~]+$/g, '');

        function parseMarkdown(text) {
            if (!text) return "";
            
            // Headers
            text = text.replace(/^#### (.*$)/gim, '<h4 class="md-h4">$1</h4>');
            text = text.replace(/^### (.*$)/gim, '<h3 class="md-h3">$1</h3>');
            text = text.replace(/^## (.*$)/gim, '<h2 class="md-h2">$1</h2>');
            text = text.replace(/^# (.*$)/gim, '<h1 class="md-h1">$1</h1>');
            
            // Bold
            text = text.replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>');
            
            // Italic
            text = text.replace(/\*(.*?)\*/g, '<em>$1</em>');
            
            // Links
            text = text.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" class="md-link">$1</a>');
            
            // Lists
            text = text.replace(/^\s*-\s+(.*$)/gim, '<li class="md-li">$1</li>');
            
            // Replace \n with <br>
            text = text.replace(/\n/g, '<br>');
            
            // Clean up <br> before/after block elements to avoid huge gaps
            text = text.replace(/(<\/h[1-4]>|<li[^>]*>.*<\/li>)<br>/g, '$1');
            text = text.replace(/<br>(<h[1-4]>|<li[^>]*>)/g, '$1');
            
            return text;
        }

        let formattedResponseText = parseMarkdown(responseText);
        let formattedThoughtText = thoughtText ? parseMarkdown(thoughtText) : "";

        if (thoughtText) {
            return `
                <div class="direct-reply">${formattedResponseText}</div>
                <div class="thought-container">
                    <div class="thought-header">
                        <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2.5" style="display: inline-block; vertical-align: middle;">
                            <path d="M9.59 4.59A2 2 0 1 1 11 8H9m10.59 11.41A2 2 0 1 1 18 16h2m-6 3h-2M6 19H4m4-7H6m14 0h-2m-8-5h2m-4 10h2M12 3v2m0 14v2M3 12h2m14 0h2"/>
                        </svg>
                        <span style="vertical-align: middle;">Pensée interne de Pixel</span>
                    </div>
                    <div class="thought-content">${formattedThoughtText}</div>
                </div>
            `;
        }

        return formattedResponseText;
    }

    chatForm.addEventListener('submit', async (e) => {
        e.preventDefault();

        // Arrêter immédiatement toute lecture vocale ou écoute active lors d'un nouvel envoi
        if (window.voiceController) {
            window.voiceController.startSpeechSession();
        }

        const text = userInput.value.trim();
        if (!text) return;

        // Désactiver l'input et le bouton d'envoi pour éviter les double-envois
        userInput.disabled = true;
        const sendBtn = chatForm.querySelector('button[type="submit"]');
        if (sendBtn) sendBtn.disabled = true;

        // Ajouter message utilisateur
        appendMessage(text, 'user');
        userInput.value = '';
        // Réinitialiser la hauteur de la textarea
        userInput.style.height = 'auto';
        userInput.style.overflowY = 'hidden';

        // Ajouter l'indicateur de chargement
        const loadingId = appendLoading();

        try {
            const response = await fetch('/api/chat', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ message: text, sender: currentUserName })
            });

            removeLoading(loadingId);

            // Create a new empty message bubble for the stream
            const msgDiv = document.createElement('div');
            msgDiv.className = `message pixel-message`;
            const avatarDiv = document.createElement('div');
            avatarDiv.className = 'avatar';
            avatarDiv.textContent = 'P';
            const bubbleDiv = document.createElement('div');
            bubbleDiv.className = 'bubble';
            msgDiv.appendChild(avatarDiv);
            msgDiv.appendChild(bubbleDiv);
            chatBox.appendChild(msgDiv);

            const reader = response.body.getReader();
            const decoder = new TextDecoder();
            let buffer = "";
            let accumulatedText = "";

            // Session déjà démarrée avant l'appel fetch

            while (true) {
                const { done, value } = await reader.read();
                if (done) break;
                
                buffer += decoder.decode(value, { stream: true });
                const lines = buffer.split('\n');
                
                // Keep the last partial line in the buffer
                buffer = lines.pop();
                
                for (const line of lines) {
                    if (line.startsWith('data: ')) {
                        const dataStr = line.substring(6).trim();
                        if (!dataStr) continue;
                        try {
                            const data = JSON.parse(dataStr);
                            if (data.error) {
                                bubbleDiv.textContent += "\nErreur système: " + data.error;
                            } else if (data.chunk) {
                                accumulatedText += data.chunk;
                                bubbleDiv.innerHTML = formatResponse(accumulatedText);
                                chatBox.scrollTop = chatBox.scrollHeight;

                                // Alimenter la synthèse vocale avec le nouveau chunk
                                if (window.voiceController) {
                                    window.voiceController.feedChunk(data.chunk);
                                }
                            }
                        } catch(e) {
                            // ignore parse errors for partial json if any
                        }
                    }
                }
            }
            
            // Process any remaining buffer
            if (buffer.startsWith('data: ')) {
                const dataStr = buffer.substring(6).trim();
                try {
                    const data = JSON.parse(dataStr);
                    if (data.chunk) {
                        accumulatedText += data.chunk;
                        bubbleDiv.innerHTML = formatResponse(accumulatedText);

                        // Alimenter la synthèse vocale finale
                        if (window.voiceController) {
                            window.voiceController.feedChunk(data.chunk);
                        }
                    }
                } catch(e) {}
            }

            // Finaliser la lecture vocale (prononcer le reliquat du buffer)
            if (window.voiceController) {
                window.voiceController.finalizeSpeech();
            }

            fetchCoreMemory(); // Mettre à jour au cas où le Profiler a agi
            fetchActiveProject(); // Mettre à jour le projet actif si besoin

            // Réactiver les entrées
            userInput.disabled = false;
            if (sendBtn) sendBtn.disabled = false;
            userInput.focus();
        } catch (err) {
            console.error("Full Error:", err);
            removeLoading(loadingId);
            appendMessage("Impossible de joindre le cerveau local. Erreur: " + err.message, 'pixel');

            if (window.voiceController) {
                window.voiceController.stopSpeaking();
                if (window.voiceController.handsfreeEnabled) {
                    setTimeout(() => {
                        if (!window.voiceController.isListening && !window.voiceController.isSpeakingSession) {
                            window.voiceController.startListening(true);
                        }
                    }, 1000);
                }
            }

            // Réactiver les entrées
            userInput.disabled = false;
            if (sendBtn) sendBtn.disabled = false;
            userInput.focus();
        }
    });

    window.appendMessage = appendMessage;

    function appendMessage(text, sender) {
        const msgDiv = document.createElement('div');
        msgDiv.className = `message ${sender}-message`;
        
        const avatarDiv = document.createElement('div');
        avatarDiv.className = 'avatar';
        avatarDiv.textContent = sender === 'pixel' ? 'P' : 'U';

        const bubbleDiv = document.createElement('div');
        bubbleDiv.className = 'bubble';
        if (sender === 'pixel') {
            bubbleDiv.innerHTML = formatResponse(text);
        } else {
            bubbleDiv.textContent = text;
        }

        msgDiv.appendChild(avatarDiv);
        msgDiv.appendChild(bubbleDiv);
        chatBox.appendChild(msgDiv);
        chatBox.scrollTop = chatBox.scrollHeight;
    }

    function appendLoading() {
        const id = 'loading-' + Date.now();
        const msgDiv = document.createElement('div');
        msgDiv.className = 'message pixel-message';
        msgDiv.id = id;

        const avatarDiv = document.createElement('div');
        avatarDiv.className = 'avatar';
        avatarDiv.textContent = 'P';

        const bubbleDiv = document.createElement('div');
        bubbleDiv.className = 'bubble loading-dots';
        bubbleDiv.innerHTML = '<div class="dot"></div><div class="dot"></div><div class="dot"></div>';

        msgDiv.appendChild(avatarDiv);
        msgDiv.appendChild(bubbleDiv);
        chatBox.appendChild(msgDiv);
        chatBox.scrollTop = chatBox.scrollHeight;
        
        return id;
    }

    function removeLoading(id) {
        const el = document.getElementById(id);
        if (el) el.remove();
    }

    async function fetchCoreMemory() {
        try {
            const res = await fetch('/api/core_memory');
            const data = await res.json();
            document.getElementById('agent-persona').textContent = data.agent_persona;
            
            const profile = data.user_profile;
            currentUserName = profile.static.name || "Marcelo";
            let html = `<strong>Nom:</strong> ${currentUserName}<br>`;
            html += `<strong>Rôle:</strong> ${profile.static.role}`;
            if (profile.volatile && Object.keys(profile.volatile).length > 0) {
                html += `<br><br><strong>État Actuel:</strong><br>`;
                for (const [k, v] of Object.entries(profile.volatile)) {
                    if (k === "Dernière vision") {
                        const visionDescEl = document.getElementById('vision-desc');
                        if (visionDescEl) {
                            visionDescEl.textContent = v;
                        }
                        continue;
                    }
                    html += `&bull; <em>${k}</em>: ${v}<br>`;
                }
            }
            if (data.dynamic_goals && data.dynamic_goals.length > 0) {
                const activeGoals = data.dynamic_goals.filter(g => g.status === 'active');
                if (activeGoals.length > 0) {
                    html += `<br><br><strong>🎯 Objectifs cognitifs :</strong><br>`;
                    for (const goal of activeGoals) {
                        html += `&bull; ${goal.description} <em>(Priorité: ${goal.priority.toFixed(1)})</em><br>`;
                    }
                }
            }
            document.getElementById('user-profile').innerHTML = html;
        } catch (e) {
            console.error("Impossible de lire la Core Memory", e);
        }
    }

    async function fetchLTMLearnings() {
        try {
            const res = await fetch('/api/ltm_learnings');
            const memories = await res.json();
            const container = document.getElementById('ltm-learnings');
            if (!memories || memories.length === 0) {
                container.innerHTML = `<span style="color: rgba(255,255,255,0.4);">Aucun apprentissage enregistré pour le moment.</span>`;
                return;
            }
            let html = "";
            memories.forEach(m => {
                const date = new Date(m.timestamp).toLocaleString('fr-FR', {
                    day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit'
                });
                
                // Color mapping for categories
                let badgeColor = "#7c3aed"; // violet for technical
                if (m.category === "Project") badgeColor = "#3b82f6"; // blue
                if (m.category === "Personal") badgeColor = "#ec4899"; // pink
                if (m.category === "Decision") badgeColor = "#ef4444"; // red
                
                html += `<div style="margin-bottom: 12px; border-bottom: 1px solid rgba(255,255,255,0.05); padding-bottom: 8px;">
                    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 4px;">
                        <span style="font-size: 0.8em; color: rgba(255,255,255,0.4);">${date}</span>
                        <span style="font-size: 0.75em; background: ${badgeColor}; color: white; padding: 1px 6px; border-radius: 4px; font-weight: 600;">${m.category}</span>
                    </div>
                    <strong style="color: #60a5fa; display: block; font-size: 0.9em; margin-bottom: 2px;">${m.title}</strong>
                    <span style="color: rgba(255,255,255,0.8);">${m.action_summary}</span>
                </div>`;
            });
            container.innerHTML = html;
        } catch (e) {
            console.error("Impossible de lire les apprentissages LTM", e);
        }
    }

    async function fetchChatHistory() {
        try {
            const res = await fetch('/api/history');
            const messages = await res.json();
            if (!messages || messages.length === 0) return;
            
            // Clear default introductory bubble if we have a real conversation going on
            chatBox.innerHTML = "";
            
            messages.forEach(msg => {
                const sender = msg.role === 'assistant' ? 'pixel' : 'user';
                appendMessage(msg.content, sender);
            });
        } catch (e) {
            console.error("Impossible de charger l'historique des messages", e);
        }
    }

    async function fetchSystemStatus() {
        try {
            const res = await fetch('/api/system_status');
            const data = await res.json();
            
            // Render internet
            const internetEl = document.getElementById('sys-internet');
            if (data.internet_active) {
                internetEl.textContent = "Actif";
                internetEl.style.color = "#10b981"; // green
            } else {
                internetEl.textContent = "Coupé";
                internetEl.style.color = "#ef4444"; // red
            }
            
            // Render RAM
            document.getElementById('sys-ram').textContent = `${data.ram_used_percent.toFixed(1)} %`;
            
            // Render CPU load
            document.getElementById('sys-cpu').textContent = `${data.load_average_1.toFixed(2)} / ${data.num_cpus}`;
            
            // Pulse color indicator
            const pulseEl = document.getElementById('health-pulse');
            pulseEl.className = ""; // clear classes
            
            const blockEl = document.getElementById('nervous-block');
            
            if (data.health_status === "Excellent") {
                pulseEl.classList.add('health-pulse-excellent');
                blockEl.style.borderLeftColor = "#10b981"; // green
            } else if (data.health_status === "Warning") {
                pulseEl.classList.add('health-pulse-warning');
                blockEl.style.borderLeftColor = "#f59e0b"; // yellow
            } else {
                pulseEl.classList.add('health-pulse-critical');
                blockEl.style.borderLeftColor = "#ef4444"; // red
            }
            
            // Render logs and auto-scroll
            const logsPre = document.getElementById('sys-logs');
            if (logsPre) {
                logsPre.textContent = data.logs;
                logsPre.scrollTop = logsPre.scrollHeight;
            }
            
        } catch (e) {
            console.error("Impossible de lire le statut système", e);
        }
    }

    async function fetchActiveProject() {
        try {
            const res = await fetch('/api/project');
            const projectTabBtn = document.getElementById('project-tab-btn');
            if (res.status === 404) {
                if (projectTabBtn) projectTabBtn.style.display = 'none';
                if (projectTabBtn && projectTabBtn.classList.contains('active')) {
                    switchToTab('tab-persona');
                }
                return;
            }
            const proj = await res.json();
            if (!proj || proj.status !== 'active') {
                if (projectTabBtn) projectTabBtn.style.display = 'none';
                if (projectTabBtn && projectTabBtn.classList.contains('active')) {
                    switchToTab('tab-persona');
                }
                return;
            }
            
            if (projectTabBtn) projectTabBtn.style.display = 'block';
            document.getElementById('project-title').textContent = proj.title;
            document.getElementById('project-central-idea').textContent = proj.central_idea;
            document.getElementById('project-goal').textContent = proj.goal;
            
            let currentStepDesc = "--";
            let stepsHtml = "";
            
            proj.steps.forEach(s => {
                let statusSymbol = "⬜";
                let style = "color: rgba(255,255,255,0.6);";
                if (s.status === "completed") {
                    statusSymbol = "✅";
                    style = "text-decoration: line-through; color: rgba(255,255,255,0.4);";
                } else if (s.id === proj.current_step_id || s.status === "active") {
                    statusSymbol = "🎯";
                    style = "color: #f59e0b; font-weight: 600;";
                    currentStepDesc = s.description;
                }
                stepsHtml += `<div style="display: flex; gap: 6px; margin-bottom: 4px; ${style}">
                    <span>${statusSymbol}</span>
                    <span>${s.description}</span>
                </div>`;
            });
            
            document.getElementById('project-current-step').textContent = currentStepDesc;
            document.getElementById('project-steps-list').innerHTML = stepsHtml;
            
        } catch (e) {
            console.error("Impossible de lire le projet actif", e);
            const projectTabBtn = document.getElementById('project-tab-btn');
            if (projectTabBtn) projectTabBtn.style.display = 'none';
        }
    }

    async function fetchTasks() {
        try {
            const res = await fetch('/api/tasks');
            const tasks = await res.json();
            
            const activeContainer = document.getElementById('active-task-content');
            const pendingList = document.getElementById('pending-tasks-list');
            const completedList = document.getElementById('completed-tasks-list');

            let activeHtml = `<span style="color: rgba(255,255,255,0.4);">Aucune tâche en cours d'exécution.</span>`;
            let pendingHtml = `<span style="color: rgba(255,255,255,0.4); font-size: 0.9em;">File d'attente vide.</span>`;
            let completedHtml = `<span style="color: rgba(255,255,255,0.4); font-size: 0.9em;">Aucun historique disponible.</span>`;

            let hasActive = false;
            let pendingTasks = [];
            let completedTasks = [];

            tasks.forEach(t => {
                if (t.status === 'running') {
                    hasActive = true;
                    // Format running task
                    activeHtml = `
                        <div style="display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 6px;">
                            <div>
                                <strong style="color: #60a5fa; font-size: 1.05em; display: block;">${t.name}</strong>
                                <span style="font-size: 0.8em; color: rgba(255,255,255,0.5);">ID: ${t.id.substring(0, 8)}...</span>
                            </div>
                            <button onclick="cancelTask('${t.id}')" style="background: rgba(239, 68, 68, 0.2); border: 1px solid rgba(239, 68, 68, 0.4); color: #fca5a5; padding: 2px 8px; border-radius: 4px; font-size: 0.8em; cursor: pointer; transition: background 0.3s;" onmouseover="this.style.background='rgba(239, 68, 68, 0.3)'" onmouseout="this.style.background='rgba(239, 68, 68, 0.2)'">Passer / Stop</button>
                        </div>
                        <div style="font-family: monospace; font-size: 0.78em; background: rgba(0,0,0,0.3); padding: 6px; border-radius: 4px; border: 1px solid rgba(255,255,255,0.05); max-height: 80px; overflow-y: auto; white-space: pre-wrap; color: #a7f3d0; margin-top: 4px;">${t.log || 'Démarrage...'}</div>
                    `;
                } else if (t.status === 'pending') {
                    pendingTasks.push(t);
                } else {
                    completedTasks.push(t);
                }
            });

            if (pendingTasks.length > 0) {
                pendingHtml = "";
                pendingTasks.forEach(t => {
                    const schedTime = new Date(t.scheduled_at).toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
                    pendingHtml += `
                        <div style="display: flex; justify-content: space-between; align-items: center; background: rgba(255,255,255,0.02); padding: 6px 8px; border-radius: 6px; border: 1px solid rgba(255,255,255,0.03);">
                            <div>
                                <strong style="color: #f59e0b; font-size: 0.9em; display: block;">${t.name}</strong>
                                <span style="font-size: 0.75em; color: rgba(255,255,255,0.4);">Planifié pour ${schedTime}</span>
                            </div>
                            <button onclick="cancelTask('${t.id}')" style="background: rgba(239, 68, 68, 0.15); border: 1px solid rgba(239, 68, 68, 0.3); color: #fca5a5; padding: 2px 6px; border-radius: 4px; font-size: 0.75em; cursor: pointer; transition: background 0.3s;" onmouseover="this.style.background='rgba(239, 68, 68, 0.25)'" onmouseout="this.style.background='rgba(239, 68, 68, 0.15)'">Retirer</button>
                        </div>
                    `;
                });
            }

            if (completedTasks.length > 0) {
                completedHtml = "";
                // Show last 5 completed tasks
                completedTasks.slice(-5).reverse().forEach(t => {
                    const timeStr = t.finished_at ? new Date(t.finished_at).toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' }) : '--:--';
                    let statusColor = "#10b981"; // completed (green)
                    let statusLabel = "Fini";
                    if (t.status === 'failed') {
                        statusColor = "#ef4444"; // failed (red)
                        statusLabel = "Échoué";
                    } else if (t.status === 'cancelled') {
                        statusColor = "#6b7280"; // cancelled (grey)
                        statusLabel = "Annulé";
                    }

                    completedHtml += `
                        <div style="background: rgba(255,255,255,0.01); padding: 6px 8px; border-radius: 6px; border: 1px solid rgba(255,255,255,0.02); display: flex; justify-content: space-between; align-items: center; font-size: 0.85em;">
                            <div>
                                <span style="font-weight: 600; color: rgba(255,255,255,0.85);">${t.name}</span>
                                ${t.error ? `<span style="color: #fca5a5; display: block; font-size: 0.8em;">Err: ${t.error}</span>` : ''}
                            </div>
                            <div style="text-align: right;">
                                <span style="font-size: 0.75em; background: ${statusColor}33; border: 1px solid ${statusColor}55; color: ${statusColor}; padding: 1px 5px; border-radius: 4px; font-weight: 600;">${statusLabel}</span>
                                <span style="display: block; font-size: 0.7em; color: rgba(255,255,255,0.3); margin-top: 2px;">${timeStr}</span>
                            </div>
                        </div>
                    `;
                });
            }

            activeContainer.innerHTML = activeHtml;
            pendingList.innerHTML = pendingHtml;
            completedList.innerHTML = completedHtml;

        } catch (e) {
            console.error("Impossible de lire les tâches du scheduler", e);
        }
    }

    // Attach cancelTask to window so it can be called from inline onclick attributes
    window.cancelTask = async function(id) {
        try {
            await fetch('/api/tasks/cancel', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ id: id })
            });
            fetchTasks();
        } catch (err) {
            console.error("Erreur d'annulation de tâche:", err);
        }
    };

    // Attach clear history button event listener
    const clearBtn = document.getElementById('clear-tasks-btn');
    if (clearBtn) {
        clearBtn.addEventListener('click', async () => {
            try {
                await fetch('/api/tasks/clear', { method: 'POST' });
                fetchTasks();
            } catch (err) {
                console.error("Erreur lors du nettoyage de l'historique:", err);
            }
        });
    }

    // Initialiser le contrôleur vocal premium (STT & TTS)
    window.voiceController = new VoiceController();

	// Interrompre la lecture vocale si l'utilisateur commence à écrire un message manuellement
    if (userInput) {
        userInput.addEventListener('input', () => {
            if (userInput.value.trim() !== "" && window.voiceController) {
                window.voiceController.stopSpeaking();
                if (window.voiceController.handsfreeEnabled) {
                    window.voiceController.setHandsfree(false);
                    window.showToast("Passage en mode clavier seul", "info");
                }
            }
            // Auto-resize de la textarea
            userInput.style.overflowY = 'hidden';
            userInput.style.height = 'auto';
            userInput.style.height = Math.min(userInput.scrollHeight, 180) + 'px';
            if (userInput.scrollHeight > 180) userInput.style.overflowY = 'auto';
        });

        // Ctrl+Entrée → saut de ligne | Entrée seul → envoyer le message
        userInput.addEventListener('keydown', (e) => {
            if (e.key === 'Enter') {
                if (e.ctrlKey) {
                    // Insérer un saut de ligne à la position du curseur
                    e.preventDefault();
                    const start = userInput.selectionStart;
                    const end = userInput.selectionEnd;
                    userInput.value = userInput.value.substring(0, start) + '\n' + userInput.value.substring(end);
                    userInput.selectionStart = userInput.selectionEnd = start + 1;
                    // Déclencher l'auto-resize
                    userInput.dispatchEvent(new Event('input'));
                } else {
                    // Entrée seul : soumettre le formulaire
                    e.preventDefault();
                    const submitBtn = chatForm.querySelector('button[type="submit"]');
                    if (submitBtn) submitBtn.click();
                    else chatForm.dispatchEvent(new Event('submit', { cancelable: true }));
                }
            }
        });
    }

    // ═══════════════════════════════════════════════════════
    //  pixelLLM — Contrôleur de configuration LLM intelligent
    // ═══════════════════════════════════════════════════════
    window.pixelLLM = {
        // Définition des fournisseurs avec leurs métadonnées
        providers: {
            'local-pixel':  { url: 'http://127.0.0.1:52625/v1',                              isCloud: false, label: 'Pixel Interne' },
            'local-ollama': { url: 'http://127.0.0.1:11434/v1',                              isCloud: false, label: 'Ollama' },
            'local-lm':     { url: 'http://127.0.0.1:1234/v1',                               isCloud: false, label: 'LM Studio' },
            'gemini':       { url: 'https://generativelanguage.googleapis.com/v1beta/openai', isCloud: true,  label: 'Google Gemini',
                              models: ['gemini-1.5-flash', 'gemini-1.5-pro', 'gemini-2.0-flash', 'gemini-2.5-flash'] },
            'openai':       { url: 'https://api.openai.com/v1',                               isCloud: true,  label: 'OpenAI',
                              models: ['gpt-4o', 'gpt-4o-mini', 'gpt-4-turbo'] },
            'groq':         { url: 'https://api.groq.com/openai/v1',                          isCloud: true,  label: 'Groq',
                              models: ['llama-3.3-70b-versatile', 'llama-3.1-8b-instant', 'mixtral-8x7b-32768'] },
            'anthropic':    { url: 'https://api.anthropic.com/v1',                            isCloud: true,  label: 'Anthropic',
                              models: ['claude-3-5-sonnet-20241022', 'claude-3-haiku-20240307', 'claude-3-opus-20240229'] },
        },

        // Cache des modèles locaux chargés depuis le serveur
        _localModels: null,

        // Charge la liste des modèles locaux depuis /api/local-models
        async loadLocalModels() {
            if (this._localModels) return this._localModels;
            try {
                const res = await fetch('/api/local-models');
                const data = await res.json();
                this._localModels = data.models || [];
            } catch(e) {
                this._localModels = [];
            }
            return this._localModels;
        },

        // Peuple un <select> de modèles
        async populateModelSelect(selectId, providerId, currentModel) {
            const sel = document.getElementById(selectId);
            if (!sel) return;
            sel.innerHTML = '<option value="">— chargement… —</option>';

            const prov = this.providers[providerId];
            let models = [];

            if (prov.isCloud) {
                models = prov.models || [];
            } else {
                models = await this.loadLocalModels();
            }

            sel.innerHTML = '';
            if (models.length === 0) {
                sel.innerHTML = '<option value="">— aucun modèle trouvé —</option>';
                return;
            }
            models.forEach(m => {
                const opt = document.createElement('option');
                opt.value = m;
                opt.textContent = m;
                if (m === currentModel) opt.selected = true;
                sel.appendChild(opt);
            });
            // Si pas de valeur sélectionnée, prendre le premier
            if (!sel.value && models.length > 0) sel.value = models[0];
        },

        // Appelé quand l'utilisateur change de fournisseur
        async onProviderChange(section, providerId) {
            const prov = this.providers[providerId];
            if (!prov) return;

            const urlDisplayId = section === 'main' ? 'main-url-display' : 'code-url-display';
            const urlHiddenId  = section === 'main' ? 'llm-local-url'    : 'llm-code-url';
            const modelSelId   = section === 'main' ? 'main-model-select' : 'code-model-select';
            const modelHidId   = section === 'main' ? 'llm-local-model'   : 'llm-code-model';

            // Mettre à jour l'URL affichée et cachée
            const urlDisplay = document.getElementById(urlDisplayId);
            const urlHidden  = document.getElementById(urlHiddenId);
            if (urlDisplay) urlDisplay.textContent = prov.url;
            if (urlHidden)  urlHidden.value = prov.url;

            // Si cloud, mettre à jour aussi cloud-url caché (pour main)
            if (section === 'main') {
                const cloudUrlHidden = document.getElementById('llm-cloud-url');
                if (cloudUrlHidden) cloudUrlHidden.value = prov.isCloud ? prov.url : 'https://generativelanguage.googleapis.com/v1beta/openai';
                // Mettre à jour le mode
                const modeHidden = document.getElementById('llm-mode-select');
                if (modeHidden) modeHidden.value = prov.isCloud ? 'cloud' : 'local';
            }

            // Charger les modèles correspondants
            const currentModel = document.getElementById(modelHidId)?.value || '';
            await this.populateModelSelect(modelSelId, providerId, currentModel);

            // Synchroniser le champ caché du modèle
            const modelSel = document.getElementById(modelSelId);
            if (modelSel) {
                document.getElementById(modelHidId).value = modelSel.value;
                if (section === 'main') document.getElementById('llm-cloud-model').value = modelSel.value;
            }

            // Afficher/masquer la section clé API si au moins un provider est cloud
            this.updateApiKeyVisibility();
        },

        // Affiche la clé API uniquement si au moins un provider est cloud
        updateApiKeyVisibility() {
            const mainProv = document.getElementById('main-provider')?.value || '';
            const codeProv = document.getElementById('code-provider')?.value || '';
            const isAnyCloud = (this.providers[mainProv]?.isCloud) || (this.providers[codeProv]?.isCloud);
            const section = document.getElementById('api-key-section');
            if (section) section.style.display = isAnyCloud ? 'block' : 'none';
        },

        // Détecte le provider à partir d'une URL
        providerFromUrl(url) {
            if (!url) return 'local-pixel';
            if (url.includes('generativelanguage.googleapis.com')) return 'gemini';
            if (url.includes('api.openai.com')) return 'openai';
            if (url.includes('api.groq.com')) return 'groq';
            if (url.includes('api.anthropic.com')) return 'anthropic';
            if (url.includes('11434')) return 'local-ollama';
            if (url.includes('1234')) return 'local-lm';
            return 'local-pixel';
        },

        // Initialise l'interface à partir des settings sauvegardés
        async init(settings) {
            // Restaurer clé API
            const keyInput = document.getElementById('llm-cloud-key');
            if (keyInput) keyInput.value = settings.cloud_api_key || '';

            // Restaurer les URLs cachées
            const localUrlH = document.getElementById('llm-local-url');
            if (localUrlH) localUrlH.value = settings.local_base_url || 'http://127.0.0.1:52625/v1';
            const cloudUrlH = document.getElementById('llm-cloud-url');
            if (cloudUrlH) cloudUrlH.value = settings.cloud_base_url || '';
            const codeUrlH = document.getElementById('llm-code-url');
            if (codeUrlH) codeUrlH.value = settings.code_base_url || 'http://127.0.0.1:52625/v1';

            // Restaurer les modèles cachés
            const localModH = document.getElementById('llm-local-model');
            if (localModH) localModH.value = settings.local_model || '';
            const cloudModH = document.getElementById('llm-cloud-model');
            if (cloudModH) cloudModH.value = settings.cloud_model || '';
            const codeModH = document.getElementById('llm-code-model');
            if (codeModH) codeModH.value = settings.code_model || '';

            // Déduire le bon provider pour chaque section
            const mainUrl = settings.active_mode === 'cloud' ? settings.cloud_base_url : settings.local_base_url;
            const mainProviderId = this.providerFromUrl(mainUrl);
            const codeProviderId = this.providerFromUrl(settings.code_base_url);

            // Sélectionner le provider dans les menus
            const mainProvSel = document.getElementById('main-provider');
            if (mainProvSel) mainProvSel.value = mainProviderId;
            const codeProvSel = document.getElementById('code-provider');
            if (codeProvSel) codeProvSel.value = codeProviderId;

            // Mettre à jour les URL affichées
            const mainUrl2 = this.providers[mainProviderId]?.url || mainUrl;
            const codeUrl2 = this.providers[codeProviderId]?.url || settings.code_base_url;
            const mainDisp = document.getElementById('main-url-display');
            if (mainDisp) mainDisp.textContent = mainUrl2;
            const codeDisp = document.getElementById('code-url-display');
            if (codeDisp) codeDisp.textContent = codeUrl2;

            // Charger les listes de modèles
            const mainCurrentModel = settings.active_mode === 'cloud' ? settings.cloud_model : settings.local_model;
            await this.populateModelSelect('main-model-select', mainProviderId, mainCurrentModel);
            await this.populateModelSelect('code-model-select', codeProviderId, settings.code_model);

            // Visibilité clé API
            this.updateApiKeyVisibility();

            // Mettre à jour l'indicateur de connexion
            const connectionEl = document.querySelector('.header-info p');
            if (connectionEl) {
                const modelName = settings.active_mode === 'cloud' ? (settings.cloud_model || 'Gemini') : (settings.local_model || 'Local');
                const modeLabel = { local: 'Local', cloud: 'Cloud', auto: 'Hybride' }[settings.active_mode] || 'Local';
                connectionEl.textContent = `Connexion neurale établie via ${modelName} (${modeLabel})`;
            }
        }
    };

    // Fonction de chargement des settings au démarrage
    async function fetchLLMSettings() {
        try {
            const res = await fetch('/api/llm_settings');
            const settings = await res.json();
            await pixelLLM.init(settings);
        } catch (e) {
            console.error("Impossible de charger les paramètres LLM", e);
        }
    }

    // Bouton Enregistrer — construit le payload à partir des champs cachés
    const saveLlmBtn = document.getElementById('save-llm-btn');
    if (saveLlmBtn) {
        saveLlmBtn.addEventListener('click', async () => {
            // Lire les providers choisis et en déduire les URLs
            const mainProvId = document.getElementById('main-provider')?.value || 'local-pixel';
            const codeProvId = document.getElementById('code-provider')?.value || 'local-pixel';
            const mainProv   = pixelLLM.providers[mainProvId];
            const codeProv   = pixelLLM.providers[codeProvId];

            // Lire les modèles choisis dans les selects
            const mainModel = document.getElementById('main-model-select')?.value || '';
            const codeModel = document.getElementById('code-model-select')?.value || '';

            // Déduire mode et URLs à partir des providers
            const isMainCloud  = mainProv?.isCloud || false;
            const activeMode   = isMainCloud ? 'cloud' : 'local';
            const localBaseURL = isMainCloud ? 'http://127.0.0.1:52625/v1' : (mainProv?.url || 'http://127.0.0.1:52625/v1');
            const cloudBaseURL = isMainCloud ? mainProv.url : 'https://generativelanguage.googleapis.com/v1beta/openai';
            const codeBaseURL  = codeProv?.url || 'http://127.0.0.1:52625/v1';
            const cloudAPIKey  = document.getElementById('llm-cloud-key')?.value || '';

            try {
                const res = await fetch('/api/llm_settings', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        active_mode:    activeMode,
                        cloud_api_key:  cloudAPIKey,
                        cloud_base_url: cloudBaseURL,
                        cloud_model:    isMainCloud ? mainModel : (document.getElementById('llm-cloud-model')?.value || ''),
                        local_base_url: localBaseURL,
                        local_model:    mainModel,
                        code_base_url:  codeBaseURL,
                        code_model:     codeModel
                    })
                });
                const data = await res.json();
                if (data.status === 'ok') {
                    window.showToast("✅ Configuration sauvegardée !", "info");
                    fetchLLMSettings();
                } else {
                    window.showToast("Erreur lors de la mise à jour", "error");
                }
            } catch (err) {
                console.error(err);
                window.showToast("Erreur réseau", "error");
            }
        });
    }
});

/* ==========================================================================
   🎤 CLASSE VOICE CONTROLLER — STT via Web Speech API + TTS via Piper/Go
   ========================================================================== */
class VoiceController {
    constructor() {
        this.isTtsEnabled     = localStorage.getItem('pixel_tts_enabled')     !== 'false';
        this.isSttEnabled     = localStorage.getItem('pixel_stt_enabled')     !== 'false'; // micro activé (indépendant du TTS)
        this.handsfreeEnabled = localStorage.getItem('pixel_handsfree_enabled') !== 'false';
        this.isListening      = false;
        this.speechQueue      = [];
        this.isSpeakingSentence = false;
        this.isSpeakingSession  = false;
        this.isStreamFinished = false;
        this.speechBuffer     = '';
        this.currentAudio     = null;

        // Vitesse / tonalité TTS
        this.voiceRate  = parseFloat(localStorage.getItem('pixel_tts_rate')  || '1.0');
        this.voicePitch = parseFloat(localStorage.getItem('pixel_tts_pitch') || '1.0');

        // Éléments DOM
        this.speakerBtn      = document.getElementById('speaker-btn');
        this.micBtn          = document.getElementById('mic-btn');
        this.voiceWave       = document.getElementById('voice-wave');
        this.chatForm        = document.getElementById('chat-form');
        this.userInput       = document.getElementById('user-input');
        this.voiceSelect     = document.getElementById('voice-select');
        this.voiceRateInput  = document.getElementById('voice-rate');
        this.voiceRateVal    = document.getElementById('voice-rate-val');
        this.voicePitchInput = document.getElementById('voice-pitch');
        this.voicePitchVal   = document.getElementById('voice-pitch-val');
        this.handsfreeInput  = document.getElementById('voice-handsfree');
        this.handsfreeToggleBtn = document.getElementById('handsfree-toggle-btn');

        this.sttEngine = localStorage.getItem('pixel_stt_engine') || 'browser';

        // Web Speech API
        const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
        if (!SR) {
            this.sttEngine = 'local';
            localStorage.setItem('pixel_stt_engine', 'local');
        }

        if (SR) {
            this.recognition = new SR();
            this.recognition.lang = 'fr-FR';
            this.recognition.continuous = false;       // une phrase à la fois
            this.recognition.interimResults = true;    // résultats en temps réel
            this.recognition.maxAlternatives = 1;
            this._bindRecognitionEvents();
        } else {
            this.recognition = null;
            console.warn('[VoiceController] Web Speech API non disponible dans ce navigateur. Passage en mode Fallback local (Whisper CPU).');
        }

        // Toujours initialiser les propriétés de fallback pour commuter à la volée
        this.audioChunks = [];
        this.mediaRecorder = null;
        this.mediaStream = null;
        this.maxDurationTimer = null;
        this._isStopping = false; // Guard anti-doublon pour stopListening

        this._setupVoiceSelect();
        this._setupUI();
    }

    /* ── Voix TTS ─────────────────────────────────────────────────────── */
    _setupVoiceSelect() {
        if (!this.voiceSelect) return;
        this.voiceSelect.innerHTML = '';
        const opt = document.createElement('option');
        opt.value = 'fr_FR-siwis-medium';
        opt.textContent = 'Voix féminine Siwis (locale)';
        opt.selected = true;
        this.voiceSelect.appendChild(opt);
        this.voiceSelect.disabled = true;
    }

    /* ── Événements Recognition ───────────────────────────────────────── */
    _bindRecognitionEvents() {
        this.recognition.onstart = () => {
            this.recognitionStartTime = Date.now();
            this.hasSpeechStarted = false;
        };

        // Résultat intermédiaire → affichage en temps réel dans le champ
        this.recognition.onresult = (event) => {
            if (this._isStopping) return;
            if (this.isSpeakingSession || this.isSpeakingSentence || this.currentAudio) {
                console.log("[VoiceController] Ignorer le résultat de reconnaissance car Pixel parle/répond.");
                this.stopListening();
                return;
            }
            this.hasSpeechStarted = true;
            let interim = '';
            let final   = '';
            for (let i = event.resultIndex; i < event.results.length; i++) {
                const t = event.results[i][0].transcript;
                if (event.results[i].isFinal) final += t;
                else interim += t;
            }

            if (this.userInput) {
                this.userInput.disabled = false;
                this.userInput.value = final || interim;
            }

            // Résultat final : envoyer si mode mains-libres
            if (final && this.handsfreeEnabled) {
                setTimeout(() => {
                    if (this.chatForm && this.userInput && this.userInput.value.trim()) {
                        const submitBtn = this.chatForm.querySelector('button[type="submit"]');
                        if (submitBtn) submitBtn.click();
                        else this.chatForm.dispatchEvent(new Event('submit', { cancelable: true }));
                    }
                }, 300);
            }
        };

        this.recognition.onspeechstart = () => {
            console.log('[VoiceController] Voix détectée.');
            this.hasSpeechStarted = true;
        };

        this.recognition.onend = () => {
            this._stopListeningUI();
            
            // Si la session a duré moins de 1.5s sans détecter de parole,
            // c'est que l'API est bloquée ou inopérante (ex. Firefox / Chromium sans clés)
            const duration = Date.now() - (this.recognitionStartTime || 0);
            if (duration < 1500 && !this.hasSpeechStarted) {
                console.warn('[VoiceController] La reconnaissance native a pris fin prématurément. Bascule définitive vers Whisper local.');
                this.sttEngine = 'local';
                localStorage.setItem('pixel_stt_engine', 'local');
                if (this.sttEngineSelect) this.sttEngineSelect.value = 'local';
                // Nullifier l'objet recognition pour que startListening() prenne
                // le chemin MediaRecorder → Whisper et non le chemin Web Speech API
                this.recognition = null;
                
                if (window.appendMessage) {
                    window.showToast('Moteur vocal navigateur indisponible — Whisper local activé', 'info');
                }
                // Ne pas relancer automatiquement : laisser l'utilisateur appuyer sur le micro
                return;
            }

            // Mode mains-libres : relancer après que Pixel a fini de parler
            if (this.handsfreeEnabled && !this.isSpeakingSession && !this.isSpeakingSentence && !this.currentAudio) {
                setTimeout(() => {
                    if (!this.isListening && !this.isSpeakingSession) this.startListening(true);
                }, 800);
            }
        };

        this.recognition.onerror = (event) => {
            // 'no-speech' est normal (timeout), pas besoin d'afficher une erreur
            if (event.error !== 'no-speech' && event.error !== 'aborted') {
                console.error('[VoiceController] Erreur reconnaissance native :', event.error);
                
                // Si l'erreur est liée au réseau ou au service Google non disponible (Chromium Linux sans clés API)
                if (event.error === 'network' || event.error === 'service-not-allowed' || event.error === 'language-not-supported') {
                    console.warn('[VoiceController] Service cloud de reconnaissance défaillant. Bascule définitive vers le moteur local.');
                    this.sttEngine = 'local';
                    localStorage.setItem('pixel_stt_engine', 'local');
                    if (this.sttEngineSelect) this.sttEngineSelect.value = 'local';
                    // Nullifier pour forcer le chemin MediaRecorder → Whisper
                    this.recognition = null;
                    
                    if (window.appendMessage) {
                        window.showToast('Moteur vocal navigateur indisponible — Whisper local activé', 'info');
                    }
                    // Ne pas relancer automatiquement : évite la boucle infinie
                    this._stopListeningUI();
                    return;
                }
                
                if (window.appendMessage && event.error === 'not-allowed') {
                    window.showToast('⚠️ Microphone refusé — autorisez l\'accès dans les paramètres du navigateur', 'warning');
                }
            }
            this._stopListeningUI();
        };
    }

    /* ── UI ───────────────────────────────────────────────────────────── */
    _setupUI() {
        // Bouton haut-parleur — contrôle UNIQUEMENT le TTS (voix de Pixel)
        if (this.speakerBtn) {
            this._updateSpeakerBtn();
            this.speakerBtn.addEventListener('click', () => {
                this.isTtsEnabled = !this.isTtsEnabled;
                localStorage.setItem('pixel_tts_enabled', this.isTtsEnabled);
                this._updateSpeakerBtn();
                if (!this.isTtsEnabled) this.stopSpeaking();
            });
        }

        // Bouton micro — clic pour démarrer / arrêter (contrôle UNIQUEMENT le STT, pas le TTS)
        if (this.micBtn) {
            this.micBtn.addEventListener('click', () => {
                if (this.isListening) this.stopListening();
                else this.startListening(false); // false = déclenché manuellement, ne coupe pas le TTS
            });
        }

        // Curseur vitesse
        if (this.voiceRateInput && this.voiceRateVal) {
            this.voiceRateInput.value = this.voiceRate;
            this.voiceRateVal.textContent = `${this.voiceRate.toFixed(1)}x`;
            this.voiceRateInput.addEventListener('input', () => {
                this.voiceRate = parseFloat(this.voiceRateInput.value);
                this.voiceRateVal.textContent = `${this.voiceRate.toFixed(1)}x`;
                localStorage.setItem('pixel_tts_rate', this.voiceRate);
            });
        }

        // Curseur tonalité
        if (this.voicePitchInput && this.voicePitchVal) {
            this.voicePitchInput.value = this.voicePitch;
            this.voicePitchVal.textContent = `${this.voicePitch.toFixed(1)}`;
            this.voicePitchInput.addEventListener('input', () => {
                this.voicePitch = parseFloat(this.voicePitchInput.value);
                this.voicePitchVal.textContent = `${this.voicePitch.toFixed(1)}`;
                localStorage.setItem('pixel_tts_pitch', this.voicePitch);
            });
        }

        // Checkbox mains-libres
        if (this.handsfreeInput) {
            this.handsfreeInput.checked = this.handsfreeEnabled;
            this.handsfreeInput.addEventListener('change', () => {
                this.setHandsfree(this.handsfreeInput.checked);
            });
        }

        // Bouton bascule mains-libres / clavier seul
        if (this.handsfreeToggleBtn) {
            this._updateHandsfreeUI();
            this.handsfreeToggleBtn.addEventListener('click', () => {
                const newState = !this.handsfreeEnabled;
                this.setHandsfree(newState);
                window.showToast(newState ? "Mode mains-libres activé" : "Mode clavier seul activé", "info");
            });
        }

        // Sélecteur de moteur STT
        this.sttEngineSelect = document.getElementById('stt-engine-select');
        if (this.sttEngineSelect) {
            this.sttEngineSelect.value = this.sttEngine;
            
            // Si le navigateur ne supporte pas l'API native (Firefox), désactiver l'option
            const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
            if (!SR) {
                const browserOpt = this.sttEngineSelect.querySelector('option[value="browser"]');
                if (browserOpt) browserOpt.disabled = true;
                this.sttEngineSelect.value = 'local';
            }
            
            this.sttEngineSelect.addEventListener('change', () => {
                this.sttEngine = this.sttEngineSelect.value;
                localStorage.setItem('pixel_stt_engine', this.sttEngine);
                console.log('[VoiceController] Commutation moteur STT :', this.sttEngine);
                this.stopListening();
            });
        }
    }

    _updateSpeakerBtn() {
        if (!this.speakerBtn) return;
        if (this.isTtsEnabled) this.speakerBtn.classList.add('active');
        else this.speakerBtn.classList.remove('active');
    }

    setHandsfree(enabled) {
        this.handsfreeEnabled = enabled;
        localStorage.setItem('pixel_handsfree_enabled', enabled);
        if (this.handsfreeInput) {
            this.handsfreeInput.checked = enabled;
        }
        this._updateHandsfreeUI();
        if (!enabled) {
            this.stopListening();
        }
    }

    _updateHandsfreeUI() {
        if (!this.handsfreeToggleBtn) return;
        if (this.handsfreeEnabled) {
            this.handsfreeToggleBtn.classList.add('active');
            this.handsfreeToggleBtn.title = "Mode Mains Libres Actif (cliquer pour forcer Clavier Seul)";
            this.handsfreeToggleBtn.innerHTML = `
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="18" height="18">
                    <path d="M12 2a3 3 0 0 0-3 3v7a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3Z"/>
                    <path d="M19 10v2a7 7 0 0 1-14 0v-2"/>
                    <path d="M12 19v4M8 23h8"/>
                    <path d="M17 5h4M17 9h2M7 5H3M5 9H3" stroke-dasharray="2 2"/>
                </svg>
            `;
        } else {
            this.handsfreeToggleBtn.classList.remove('active');
            this.handsfreeToggleBtn.title = "Mode Clavier Seul Actif (cliquer pour activer Mains Libres)";
            this.handsfreeToggleBtn.innerHTML = `
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="18" height="18">
                    <rect x="2" y="4" width="20" height="16" rx="2" ry="2"/>
                    <path d="M6 8h.01M10 8h.01M14 8h.01M18 8h.01M6 12h.01M10 12h.01M14 12h.01M18 12h.01M7 16h10"/>
                </svg>
            `;
        }
    }

    /* ── STT ───────────────────────────────────────────────── */
    // fromHandsfree : true = appel automatique depuis le système mains-libres (coupe le TTS pour éviter l'echo)
    //                 false = clic manuel de l'utilisateur (le TTS continue si activé)
    async startListening(fromHandsfree = false) {
        if (fromHandsfree && (this.isSpeakingSession || this.isSpeakingSentence || this.currentAudio)) {
            console.log("[VoiceController] Refus de démarrer l'écoute automatique mains-libres pendant que Pixel parle.");
            return;
        }

        this.stopSpeaking();

        // Réinitialiser le guard anti-doublon à chaque nouvelle session d'écoute
        this._isStopping = false;

        // Réactiver le champ texte
        if (this.userInput) {
            this.userInput.disabled = false;
            this.userInput.value = '';
            this.userInput.placeholder = '🎤 Parlez maintenant...';
        }

        if (this.recognition) {
            try {
                this.recognition.start();
                this.isListening = true;
                this._startListeningUI();
            } catch (e) {
                // Déjà en cours → on ignore
                console.warn('[VoiceController] Recognition déjà active :', e.message);
            }
            return;
        }

        // Fallback local (MediaRecorder -> backend Whisper)
        this.audioChunks = [];
        try {
            const stream = await navigator.mediaDevices.getUserMedia({
                audio: {
                    echoCancellation: true,
                    noiseSuppression: true,
                    autoGainControl: true
                }
            });
            this.mediaStream = stream;

            let options = {};
            if (typeof MediaRecorder.isTypeSupported === 'function') {
                if (MediaRecorder.isTypeSupported('audio/webm')) {
                    options.mimeType = 'audio/webm';
                } else if (MediaRecorder.isTypeSupported('audio/ogg')) {
                    options.mimeType = 'audio/ogg';
                } else if (MediaRecorder.isTypeSupported('audio/mp4')) {
                    options.mimeType = 'audio/mp4';
                }
            }
            this.mediaRecorder = new MediaRecorder(stream, options);

            this.mediaRecorder.ondataavailable = (event) => {
                if (event.data && event.data.size > 0) {
                    this.audioChunks.push(event.data);
                }
            };

            this.mediaRecorder.onstop = () => {
                this.uploadAudioAndTranscribe();
            };

            this.mediaRecorder.start();
            this.recordingStartTime = Date.now();
            this.isListening = true;
            this._startListeningUI();

            // Sécurité : arrêt automatique après 15 secondes max
            this.maxDurationTimer = setTimeout(() => {
                if (this.isListening) {
                    console.log('[VoiceController] Durée max 15s, arrêt automatique.');
                    this.stopListening();
                }
            }, 15000);

            // Détecter la fin de parole via le volume (uniquement en mains-libres)
            this.setupSilenceDetection(stream);

        } catch (err) {
            console.error("Impossible d'accéder au microphone :", err);
            // Désactiver le mode mains-libres pour éviter les boucles infinies de popups
            this.handsfreeEnabled = false;
            if (this.handsfreeInput) this.handsfreeInput.checked = false;
            localStorage.setItem('pixel_handsfree_enabled', 'false');

            if (window.appendMessage) {
                window.showToast('⚠️ Micro inaccessible : ' + err.message, 'warning');
            }
            this.stopListeningUI();
        }
    }

    stopListening() {
        // Guard anti-doublon : évite que VAD + bouton/timeout déclenchent deux uploads simultanés
        if (this._isStopping) return;
        this._isStopping = true;

        if (this.recognition && this.isListening) {
            try { this.recognition.abort(); } catch(e) {}
        }
        if (this.mediaRecorder && this.isListening) {
            try { this.mediaRecorder.stop(); } catch(e) {}
            if (this.mediaStream) {
                this.mediaStream.getTracks().forEach(track => track.stop());
            }
        }
        if (this.maxDurationTimer) {
            clearTimeout(this.maxDurationTimer);
            this.maxDurationTimer = null;
        }
        this._stopListeningUI();
    }

    _startListeningUI() {
        if (this.micBtn)    this.micBtn.classList.add('listening');
        if (this.chatForm)  this.chatForm.classList.add('listening-glow');
        if (this.voiceWave) {
            this.voiceWave.style.display = 'flex';
            setTimeout(() => this.voiceWave.style.opacity = '1', 10);
        }
    }

    _stopListeningUI() {
        this.isListening = false;
        if (this.userInput) this.userInput.placeholder = 'Écrivez un message ou parlez...';
        if (this.micBtn)    this.micBtn.classList.remove('listening');
        if (this.chatForm)  this.chatForm.classList.remove('listening-glow');
        if (this.voiceWave) {
            this.voiceWave.style.opacity = '0';
            setTimeout(() => { if (!this.isListening) this.voiceWave.style.display = 'none'; }, 300);
        }
    }

    // Alias conservés pour compatibilité avec le code existant
    stopListeningUI() { this._stopListeningUI(); }

    setupSilenceDetection(stream) {
        // Actif dans TOUS les modes (manuel et mains-libres) — l'utilisateur n'a plus besoin
        // d'appuyer sur le bouton pour arrêter : le silence automatique déclenche la transcription
        try {
            this.audioCtx = new (window.AudioContext || window.webkitAudioContext)();
            this.analyser = this.audioCtx.createAnalyser();
            this.source = this.audioCtx.createMediaStreamSource(stream);
            
            this.source.connect(this.analyser);
            this.analyser.fftSize = 256;
            
            const bufferLength = this.analyser.frequencyBinCount;
            const dataArray = new Uint8Array(bufferLength);
            
            let silenceStart = null;
            let speechStartTime = null;
            let speechDetected = false;
            this.isSilenceDetectionActive = true;
            
            // Délai de silence avant arrêt : 1.2s en manuel (réactif), 2s en mains-libres (confortable)
            const silenceDelay = this.handsfreeEnabled ? 2000 : 1200;
            // Parole minimum avant de considérer un silence comme "fin de phrase"
            const minSpeechMs = 400;
            
            const checkVolume = () => {
                if (!this.isSilenceDetectionActive || !this.isListening) return;
                
                this.analyser.getByteFrequencyData(dataArray);
                
                let sum = 0;
                for (let i = 0; i < bufferLength; i++) {
                    sum += dataArray[i];
                }
                const averageVolume = sum / bufferLength;
                
                const threshold = 8; // Sensibilité augmentée (était 12) — capte mieux les voix douces
                
                if (averageVolume > threshold) {
                    if (!speechDetected) speechStartTime = Date.now();
                    speechDetected = true;
                    silenceStart = null;
                } else if (speechDetected) {
                    if (silenceStart === null) {
                        silenceStart = Date.now();
                    } else {
                        const silenceDuration = Date.now() - silenceStart;
                        const speechDuration = silenceStart - (speechStartTime || silenceStart);
                        if (silenceDuration > silenceDelay && speechDuration > minSpeechMs) {
                            console.log(`[VoiceController] Fin de parole (${speechDuration}ms discours, ${silenceDuration}ms silence) → transcription`);
                            this.stopListening();
                            return;
                        }
                    }
                }
                requestAnimationFrame(checkVolume);
            };
            checkVolume();
        } catch (e) {
            console.warn('VAD non disponible :', e);
        }
    }

    async uploadAudioAndTranscribe() {
        if (this.audioChunks.length === 0) return;

        // Si Pixel est déjà en train de parler ou de répondre, on ignore la transcription
        if (this.isSpeakingSession || this.isSpeakingSentence || this.currentAudio) {
            console.log("[VoiceController] Pixel répond déjà. On ignore la transcription de l'audio résiduel.");
            this.audioChunks = [];
            return;
        }
        
        // Vider les chunks immédiatement pour éviter un 2ème upload si onstop est rappelé
        const chunks = this.audioChunks.splice(0);

        const recordingDuration = Date.now() - (this.recordingStartTime || 0);
        if (recordingDuration < 800) {
            console.log(`[VoiceController] Enregistrement trop court (${recordingDuration}ms), ignoré.`);
            return;
        }

        const mimeType = (this.mediaRecorder && this.mediaRecorder.mimeType) || 'audio/webm';
        const audioBlob = new Blob(chunks, { type: mimeType });
        const formData = new FormData();
        formData.append('file', audioBlob, 'mic.webm');

        if (this.userInput) {
            this.userInput.value = "... (Pixel décode votre voix) ...";
        }

        try {
            const response = await fetch('/api/voice/transcribe', {
                method: 'POST',
                body: formData
            });

            if (!response.ok) {
                throw new Error("Le serveur local Go a renvoyé une erreur de transcription");
            }

            const data = await response.json();
            if (this.userInput) {
                if (data.text && data.text.trim()) {
                    const cleaned = data.text.trim();
                    
                    // Filtrer les hallucinations
                    const isHallucination = cleaned.length < 3 || /^(.{10,50})\1{2,}/.test(cleaned);
                    if (isHallucination) {
                        console.warn("[VoiceController] Hallucination Whisper ignorée:", cleaned);
                        this.userInput.value = "";
                        if (this.handsfreeEnabled && !this.isSpeakingSession) {
                            setTimeout(() => this.startListening(true), 600);
                        }
                        return;
                    }
                    
                    this.userInput.value = cleaned;
                    if (this.handsfreeEnabled) {
                        if (this.isSpeakingSession || this.isSpeakingSentence || this.currentAudio) {
                            console.warn("[VoiceController] Pixel a commencé à parler pendant la transcription. Annulation de l'envoi.");
                            this.userInput.value = "";
                            return;
                        }
                        console.log('[VoiceController] Mode mains-libres : envoi automatique de la transcription :', cleaned);
                        setTimeout(() => {
                            if (this.chatForm && this.userInput && this.userInput.value.trim() === cleaned) {
                                const submitBtn = this.chatForm.querySelector('button[type="submit"]');
                                if (submitBtn) {
                                    submitBtn.click();
                                } else {
                                    this.chatForm.dispatchEvent(new Event('submit', { cancelable: true }));
                                }
                            }
                        }, 300);
                    } else {
                        // Mettre le focus et sélectionner tout le texte pour correction facile
                        this.userInput.focus();
                        this.userInput.select();
                        // Indication visuelle : bordure orange pour signaler "en attente de validation"
                        this.userInput.style.outline = '2px solid #f59e0b';
                        this.userInput.style.boxShadow = '0 0 8px rgba(245,158,11,0.5)';
                        const clearStyle = () => {
                            this.userInput.style.outline = '';
                            this.userInput.style.boxShadow = '';
                        };
                        // Retirer le highlight dès que l'utilisateur touche l'input ou envoie
                        this.userInput.addEventListener('keydown', clearStyle, { once: true });
                        this.userInput.addEventListener('input', clearStyle, { once: true });
                        console.log('[VoiceController] Transcription prête pour correction :', cleaned);
                    }
                } else {
                    this.userInput.value = "";
                    if (window.appendMessage) {
                        window.showToast('Aucune voix détectée — signal trop faible, réessayez', 'info');
                    }
                }
            }
        } catch (err) {
            console.error("Échec de la transcription locale Go :", err);
            if (this.userInput) this.userInput.value = "";
            if (window.appendMessage) {
                window.showToast('⚠️ Échec transcription : ' + err.message, 'error');
            }
        }
    }

    /* ── TTS (inchangé — Piper/Go) ────────────────────────────────────── */
    startSpeechSession() {
        this.stopListening();
        this.stopSpeaking();
        this.isSpeakingSession = true;
        this.speechQueue      = [];
        this.speechBuffer     = '';
        this.isSpeakingSentence = false;
        this.isStreamFinished = false;
    }

    feedChunk(chunk) {
        if (!this.isTtsEnabled) return;
        this.speechBuffer += chunk;

        while (true) {
            let boundary = -1;
            for (let i = 0; i < this.speechBuffer.length; i++) {
                const c = this.speechBuffer[i];
                if (c === '.' || c === '?' || c === '!' || c === '\n') {
                    if (c === '.' && i + 1 < this.speechBuffer.length && /\d/.test(this.speechBuffer[i+1])) continue;
                    boundary = i;
                    break;
                }
            }

            if (boundary !== -1) {
                let sentence = this.speechBuffer.substring(0, boundary + 1)
                    .replace(/\*\*|\*|`|###|##|#/g, '').trim();
                this.speechBuffer = this.speechBuffer.substring(boundary + 1);
                if (sentence.length > 1) {
                    this.speechQueue.push(sentence);
                    this._playNext();
                }
            } else {
                break;
            }
        }
    }

    finalizeSpeech() {
        this.isStreamFinished = true;
        if (!this.isTtsEnabled) {
            this.isSpeakingSession = false;
            if (this.handsfreeEnabled && !this.isListening) {
                setTimeout(() => {
                    if (!this.isListening && !this.isSpeakingSession) this.startListening(true);
                }, 1000);
            }
            return;
        }
        const remaining = this.speechBuffer.replace(/\*\*|\*|`|###|##|#/g, '').trim();
        if (remaining.length > 1) this.speechQueue.push(remaining);
        this.speechBuffer = '';
        this._playNext();
    }

    _playNext() {
        if (!this.isTtsEnabled || this.isSpeakingSentence) return;

        if (this.speechQueue.length === 0) {
            if (this.isStreamFinished && !this.currentAudio) {
                this.isSpeakingSession = false;
                // Mains-libres : relancer l'écoute après que Pixel a fini de parler
                if (this.handsfreeEnabled && !this.isListening) {
                    setTimeout(() => {
                        if (!this.currentAudio && !this.isListening && !this.isSpeakingSession) this.startListening(true); // true = mains-libres
                    }, 650);
                }
            }
            return;
        }

        this.isSpeakingSentence = true;

        const sentence = this.speechQueue.shift();
        const url = `/api/voice/speak?text=${encodeURIComponent(sentence)}`;
        const audio = new Audio(url);
        this.currentAudio = audio;
        audio.playbackRate = this.voiceRate;

        const onEnd   = () => {
            if (this.currentAudio !== audio) return; // Ignore if audio was replaced or stopped
            this.isSpeakingSentence = false;
            this.currentAudio = null;
            setTimeout(() => this._playNext(), 80);
        };
        audio.onended = onEnd;
        audio.onerror = onEnd;
        audio.play().catch(onEnd);
    }

    stopSpeaking() {
        if (this.currentAudio) {
            try { this.currentAudio.pause(); } catch(e) {}
            this.currentAudio = null;
        }
        this.speechQueue      = [];
        this.speechBuffer     = '';
        this.isSpeakingSentence = false;
        this.isSpeakingSession  = false;
    }
}

