<img src="docs/images/ollamapaca-logo.png" width="120" alt="Logo d'Ollamapaca, un alpaga en traits noirs avec une pièce de puzzle multicolore">

# Ollamapaca

[English](README.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · **Français** · [日本語](README.ja.md)

Ollamapaca, c'est Ollama avec des plugins. Un modèle ouvert, en local, peut piloter votre vrai Chrome, utiliser n'importe quel serveur MCP et charger des plugins Codex et Claude, le tout sur votre machine, sans service hébergé.

C'est un fork de [ollama/ollama](https://github.com/ollama/ollama). L'application Mac s'appelle `Ollamapaca.app` (bundle id `ai.ollamapaca.app`). Elle cohabite avec une installation normale d'Ollama : elle réutilise le serveur Ollama déjà lancé, partage vos modèles et garde ses propres conversations.

Dépôt compagnon : [ollama-plugin-seed](https://github.com/richardsondx/ollama-plugin-seed), avec l'extension Chrome et l'exécuteur local de plugins.

## Ce que vous obtenez

- **Le pilotage de votre vrai Chrome.** Le modèle ouvre des onglets, lit les pages, clique, tape du texte, appuie sur des touches et fait défiler, grâce à l'extension Chrome d'Ollamapaca. Pas besoin de `--remote-debugging-port`.
- **Des serveurs MCP, comme dans Codex et Claude.** Ajoutez n'importe quel serveur MCP local (stdio) ou distant (streamable HTTP ou SSE) dans **Plugins → MCP**, ou collez un extrait JSON `mcpServers`.
- **Les plugins Codex et Claude.** Les dossiers contenant `.codex-plugin/plugin.json` ou `.claude-plugin/plugin.json` se chargent, avec leurs serveurs `.mcp.json` et leurs skills.
- **Les mentions `@`.** Tapez `@browser` ou `@<serveur-mcp>` dans le chat pour ne donner que ces outils au modèle pour un message.

## Démarrage rapide (Mac)

1. **Compilez et ouvrez l'application**, avec Go 1.26 et Node 22 :

   ```bash
   git clone https://github.com/richardsondx/ollamapaca && cd ollamapaca
   (cd app/ui/app && npm install && npm run build)
   go build -o Ollamapaca ./app/cmd/app && ./Ollamapaca
   ```

   Laissez Ollama tourner (l'application ou `ollama serve`). Ollamapaca l'utilise.

2. **Récupérez l'exécuteur de plugins**, que l'application lance elle-même au besoin :

   ```bash
   git clone https://github.com/richardsondx/ollama-plugin-seed ~/.ollamapaca/seed
   ```

3. **Chargez l'extension Chrome.** Ouvrez `chrome://extensions`, activez le **Mode développeur**, cliquez sur **Charger l'extension non empaquetée** et choisissez `~/.ollamapaca/seed/chrome-extension` (appuyez sur Cmd+Maj+G dans le sélecteur de dossier pour coller le chemin). Sa fenêtre doit indiquer qu'elle est connectée.

4. **Activez le plugin navigateur.** Dans Ollamapaca, ouvrez **Plugins**, sélectionnez **browser** et cliquez sur **Enable**.

5. **Choisissez un modèle capable d'appeler des outils**, par exemple `qwen3`, `qwen2.5:7b-instruct`, `llama3.1`, `gpt-oss`, ou un modèle cloud comme `glm-5.2:cloud`. La page Plugins indique lesquels de vos modèles installés savent appeler des outils.

## Essayez : des prompts à copier-coller

Chaque prompt ci-dessous fait agir le modèle dans votre Chrome. Vous voyez l'onglet s'ouvrir, et le chat affiche chaque étape (ouvrir, cliquer, taper) avec un bouton de détails, puis la réponse.

**Navigateur**

```text
@browser ouvre news.ycombinator.com et dis-moi quel est l'article en tête
```

```text
@browser va sur fr.wikipedia.org, cherche « Alpaga » et donne-moi la première phrase de l'article
```

```text
@browser ouvre github.com/ollama/ollama et dis-moi combien il a d'étoiles
```

```text
@browser ouvre duckduckgo.com, cherche « meilleures randonnées près de Montréal » et donne-moi les trois premiers résultats
```

```text
@browser liste mes onglets ouverts, puis passe à celui qui parle de GitHub
```

**MCP**

Dans **Plugins → MCP**, choisissez **Paste JSON**, collez ceci et cliquez sur **Add and connect** :

```json
{
  "mcpServers": {
    "memory": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-memory"] }
  }
}
```

Quand il affiche **Ready**, essayez :

```text
@memory retiens que mon animal préféré est l'alpaga
```

```text
@memory quel est mon animal préféré ?
```

Un serveur distant fonctionne de la même façon : choisissez **URL** et saisissez son adresse, par exemple `https://example.com/mcp`.

**Ordinateur (Mac)**

```text
@computer liste les applications que je peux contrôler
```

Les actions sur l'ordinateur sont simulées tant que vous n'activez pas le mode réel. Voir [docs/PLUGINS-AND-MCP.md](docs/PLUGINS-AND-MCP.md).

### Le prouver avec un test

Le dépôt contient un test de bout en bout qui envoie un vrai prompt à un vrai modèle via le code de chat de l'application, avec les vrais plugins :

```bash
OLLAMAPACA_E2E_MODEL=qwen2.5:7b-instruct OLLAMAPACA_E2E_PLUGINS=browser \
OLLAMAPACA_E2E_PROMPT='@browser go to en.wikipedia.org, search for "Llama", and tell me the first sentence of the article' \
go test ./app/ui -run TestPluginChatEndToEnd -v
```

Il affiche chaque appel d'outil, chaque résultat et la réponse du modèle, et échoue si le modèle n'a appelé aucun outil. Pour un prompt navigateur, l'exécuteur doit tourner et l'extension Chrome être connectée.

## Limites

- Les pages sont lues sous forme de texte et de listes d'éléments numérotés. Aucune capture d'écran n'est envoyée au modèle, et les iframes et le shadow DOM ne sont pas encore listés.
- Les petits modèles écrivent parfois des appels d'outils qu'Ollama standard ne sait pas lire quand un outil a une très longue description. L'application raccourcit les descriptions et réessaie, mais la vraie solution est un modèle plus doué pour les outils.
- Pas encore de commandes de plugins en ligne de commande.

## Sécurité par défaut

- Les plugins sont désactivés tant que vous ne les activez pas. Un plugin désactivé ne donne aucun outil au modèle.
- Le pilotage du navigateur passe uniquement par l'extension Chrome que vous chargez vous-même, ne parle qu'à 127.0.0.1 et se désactive dans sa fenêtre. Il ne lit jamais les cookies ni les mots de passe enregistrés, et ne renvoie jamais le contenu des champs de mot de passe.
- Les actions sur l'ordinateur sont simulées par défaut. Le vrai contrôle du Mac demande `OLLAMA_COMPUTER_USE_REAL=1` et l'autorisation Accessibilité, et les actions risquées demandent toujours une confirmation.
- Le texte venant des pages web et des outils est une donnée, jamais une instruction. Il ne peut ajouter ni plugin, ni destination, ni permission.

## Pour aller plus loin

- Guide complet (en anglais) : [docs/PLUGINS-AND-MCP.md](docs/PLUGINS-AND-MCP.md)
- Documentation d'origine d'Ollama : seconde moitié de [README.md](README.md)
