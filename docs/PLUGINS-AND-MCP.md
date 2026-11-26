# Plugins and MCP servers in Ollamapaca

Ollamapaca gives a local model tools the way Codex and Claude do: plugins, and any MCP server you connect. Everything runs on your machine.

## How it fits together

```
Ollamapaca desktop app (Go)
├── Plugins page and chat ─────────────── owns plugin list, enable state, MCP config
├── MCP client ────────────────────────── stdio, streamable HTTP, legacy SSE
│     ├── servers you add on the MCP tab   (~/.ollamapaca/mcp.json)
│     └── servers bundled in plugins       (.mcp.json, Codex and Claude layouts)
└── plugin executor, 127.0.0.1:11435 ───── ollama-plugin-seed (Python, stdlib only)
      ├── browser tools ─── Ollamapaca Chrome extension ─── your real Chrome
      └── computer tools ── Mac adapter (dry run unless OLLAMA_COMPUTER_USE_REAL=1)

Any ollama server on 11434 (this fork or a stock install) runs the model.
```

The desktop app no longer asks the Ollama server for plugins, so plugins work with whichever `ollama` is running.

## Browser control, step by step

1. Get the seed: `git clone https://github.com/richardsondx/ollama-plugin-seed ~/.ollamapaca/seed`. The app starts it automatically from there the first time a browser tool runs. You can also start it yourself with `python3 scripts/run_server.py`, or point `OLLAMAPACA_SEED_DIR` at another checkout.
2. In Chrome, open `chrome://extensions`, turn on Developer mode, click **Load unpacked**, and pick `~/.ollamapaca/seed/chrome-extension`. The popup should say it is connected. After you update the extension files, click the reload icon on its card.
3. In Ollamapaca, open **Plugins**, select **browser**, and click **Enable**.
4. In chat, pick a model that can call tools, and ask, for example: `@browser open news.ycombinator.com and tell me the top story`.

The model gets these tools: `create_tab`, `navigate`, `read_page`, `click`, `type`, `press_key`, `scroll`, `go_back`, `get_text`, `list_tabs`, `switch_tab`, `close_tab`, plus the WebMCP tools. Every action returns the page as numbered interactive elements (`[7] input "Search"`) and visible text, so the model clicks and types by number and never guesses coordinates. Clicks and key presses go through `chrome.debugger`, so they are real input events. Chrome shows a "started debugging this browser" bar while one runs, and the extension detaches 20 seconds after the last action. You can turn browser control off in the extension popup.

Tested with `qwen2.5:7b-instruct` running locally (open a site, search, read the result) and with `glm-5.2:cloud`. Bigger or newer tool-calling models do better on long tasks.

## Adding MCP servers

Open **Plugins → MCP**. You can add a server in three ways:

- **Command**: a stdio server, for example `npx -y @modelcontextprotocol/server-memory`, with optional environment variables.
- **URL**: a remote server. Streamable HTTP is tried first, and old HTTP+SSE servers are detected automatically. You can add headers, for example `Authorization: Bearer …`.
- **Paste JSON**: the usual `{"mcpServers": {...}}` snippet from a server's README.

Servers are saved in `~/.ollamapaca/mcp.json`, start on first use, are warmed up when the app starts, and restart if they crash. Their tools show up in chat as `mcp__<server>__<tool>`. Type `@<server>` in chat to use only that server for one turn. With no @ mention, every enabled plugin and MCP server is available.

The app finds `npx`, `uvx`, `docker` and other commands through your login shell's PATH, so servers that work in your terminal work here too.

## Plugins

A plugin is a folder in `~/.ollamapaca/plugins` (yours, listed first), `OLLAMA_PLUGINS_DIR`, or the folders bundled with the app. Three layouts are understood:

| Layout | Manifest | Tools come from |
| --- | --- | --- |
| Ollamapaca | `plugin.json` + `SKILL.md` | `tools` in the manifest, run by the executor |
| Codex | `.codex-plugin/plugin.json` | `mcpServers` (inline, a path, or `.mcp.json`) |
| Claude | `.claude-plugin/plugin.json` | same as Codex |

`skills/<name>/SKILL.md` files are read too. While a plugin's tools are active, its skill text is given to the model as instructions. `${CLAUDE_PLUGIN_ROOT}`, `${CODEX_PLUGIN_ROOT}` and `${PLUGIN_ROOT}` in MCP commands are replaced with the plugin folder.

New plugin folders show up without a restart.

## Settings

| Variable | Default | What it does |
| --- | --- | --- |
| `OLLAMAPACA_HOME` | `~/.ollamapaca` | user plugins, plugin state, MCP config |
| `OLLAMAPACA_MCP_CONFIG` | `$OLLAMAPACA_HOME/mcp.json` | MCP server list |
| `OLLAMA_PLUGINS_DIR` | none | extra plugin folders, `:`-separated |
| `OLLAMA_PLUGIN_STATE` | `$OLLAMAPACA_HOME/plugins-state.json` | which plugins are enabled |
| `OLLAMA_PLUGIN_EXECUTOR_URL` | `http://127.0.0.1:11435` | plugin executor (local addresses only) |
| `OLLAMAPACA_SEED_DIR` | `~/.ollamapaca/seed` | where the app looks for the executor to start |

## Testing it yourself

```sh
go test ./plugins ./app/tools ./app/ui
```

The end-to-end chat test runs the real chat loop against a real model. It needs the seed running and the Chrome extension connected for browser prompts:

```sh
OLLAMAPACA_E2E_MODEL=qwen2.5:7b-instruct OLLAMAPACA_E2E_PLUGINS=browser \
OLLAMAPACA_E2E_PROMPT='@browser open en.wikipedia.org and search for Llama' \
go test ./app/ui -run TestPluginChatEndToEnd -v
```

## Known limits

- Some small models write tool calls that stock Ollama's parser drops when a tool has a very long description. The app shortens MCP tool descriptions and retries an empty reply, but a stronger tool-calling model is the real fix.
- No screenshots are sent to the model yet. Pages are read as text and element lists.
- Pages inside iframes and shadow DOM are not listed in `read_page` yet.
