import { useEffect, useState } from "react";
import { API_BASE } from "@/lib/config";

// MCP servers the user adds here are saved in ~/.ollamapaca/mcp.json
// by the desktop app, started on first use, and their tools are
// offered to tool-capable models in chat, like Codex and Claude do.

type McpTool = { name: string; description?: string };

type McpServer = {
  key: string;
  name: string;
  plugin?: string;
  transport: string;
  state: "idle" | "starting" | "ready" | "error" | "disabled";
  error?: string;
  tools: McpTool[];
  config: {
    command?: string;
    args?: string[];
    url?: string;
    type?: string;
    disabled?: boolean;
  };
};

type Mode = "stdio" | "http" | "json";

const EXAMPLE_JSON = `{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "~/Documents"]
    }
  }
}`;

// splitArgs splits a command line on spaces, keeping quoted parts.
function splitArgs(line: string): string[] {
  const out: string[] = [];
  const re = /"([^"]*)"|'([^']*)'|(\S+)/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(line)) !== null) out.push(m[1] ?? m[2] ?? m[3]);
  return out;
}

function parsePairs(text: string, sep: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const i = line.indexOf(sep);
    if (i <= 0) continue;
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

async function readError(response: Response): Promise<string> {
  const text = await response.text();
  try {
    const data = JSON.parse(text);
    return data.error || text;
  } catch {
    return text || `HTTP ${response.status}`;
  }
}

function StateBadge({ server }: { server: McpServer }) {
  const styles: Record<string, string> = {
    ready: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300",
    error: "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300",
    starting: "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
    idle: "bg-neutral-200 text-neutral-700 dark:bg-neutral-700 dark:text-neutral-200",
    disabled: "bg-neutral-200 text-neutral-500 dark:bg-neutral-700 dark:text-neutral-400",
  };
  const label: Record<string, string> = {
    ready: `Ready · ${server.tools.length} tools`,
    error: "Error",
    starting: "Starting…",
    idle: "Not started",
    disabled: "Disabled",
  };
  return (
    <span className={`rounded-full px-2 py-0.5 text-xs ${styles[server.state] || styles.idle}`}>
      {label[server.state] || server.state}
    </span>
  );
}

export function McpServers() {
  const [servers, setServers] = useState<McpServer[]>([]);
  const [configPath, setConfigPath] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  const [mode, setMode] = useState<Mode>("stdio");
  const [name, setName] = useState("");
  const [command, setCommand] = useState("");
  const [url, setUrl] = useState("");
  const [env, setEnv] = useState("");
  const [headers, setHeaders] = useState("");
  const [json, setJson] = useState("");
  const [formError, setFormError] = useState<string | null>(null);

  const load = async () => {
    try {
      const response = await fetch(`${API_BASE}/api/v1/mcp`);
      if (!response.ok) throw new Error(await readError(response));
      const data = await response.json();
      setServers(data.servers || []);
      setConfigPath(data.configPath || "");
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not load MCP servers");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
    // Servers start in the background, so poll while any is starting.
    const timer = setInterval(() => void load(), 4000);
    return () => clearInterval(timer);
  }, []);

  const add = async () => {
    setFormError(null);
    let body: unknown;
    if (mode === "json") {
      try {
        const parsed = JSON.parse(json);
        body = parsed.mcpServers ? parsed : { mcpServers: parsed };
      } catch (err) {
        setFormError("That is not valid JSON: " + (err instanceof Error ? err.message : ""));
        return;
      }
    } else {
      if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(name)) {
        setFormError("Name: letters, digits, - and _ only, for example filesystem.");
        return;
      }
      if (mode === "stdio") {
        const parts = splitArgs(command.trim());
        if (parts.length === 0) {
          setFormError("Enter the command that starts the server, for example npx -y @modelcontextprotocol/server-memory");
          return;
        }
        body = { name, config: { command: parts[0], args: parts.slice(1), env: parsePairs(env, "=") } };
      } else {
        if (!/^https?:\/\//.test(url.trim())) {
          setFormError("Enter the server URL, starting with http:// or https://");
          return;
        }
        body = { name, config: { url: url.trim(), headers: parsePairs(headers, ":") } };
      }
    }
    setBusy("add");
    try {
      const response = await fetch(`${API_BASE}/api/v1/mcp`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!response.ok) throw new Error(await readError(response));
      const data = await response.json();
      setServers(data.servers || []);
      setName("");
      setCommand("");
      setUrl("");
      setEnv("");
      setHeaders("");
      setJson("");
    } catch (err) {
      setFormError(err instanceof Error ? err.message : "Could not add the server");
    } finally {
      setBusy(null);
    }
  };

  const act = async (server: McpServer, action: "enable" | "disable" | "restart" | "delete") => {
    setBusy(server.key + action);
    try {
      const response =
        action === "delete"
          ? await fetch(`${API_BASE}/api/v1/mcp/${encodeURIComponent(server.name)}`, { method: "DELETE" })
          : await fetch(`${API_BASE}/api/v1/mcp/${encodeURIComponent(server.name)}/${action}`, { method: "POST" });
      if (!response.ok) throw new Error(await readError(response));
      const data = await response.json();
      setServers(data.servers || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : `Could not ${action} ${server.name}`);
    } finally {
      setBusy(null);
    }
  };

  const userServers = servers.filter((s) => !s.plugin);
  const pluginServers = servers.filter((s) => s.plugin);
  const input =
    "w-full rounded-lg border border-neutral-200 bg-white px-3 py-2 text-sm outline-none focus:border-neutral-400 dark:border-neutral-700 dark:bg-neutral-900";
  const modeClass = (m: Mode) =>
    `rounded-full px-3 py-1 text-xs ${
      mode === m
        ? "bg-neutral-900 text-white dark:bg-white dark:text-black"
        : "text-neutral-600 hover:bg-neutral-200 dark:text-neutral-300 dark:hover:bg-neutral-700"
    }`;

  const renderServer = (server: McpServer, removable: boolean) => (
    <li key={server.key} className="rounded-xl border border-neutral-200 px-4 py-3 dark:border-neutral-700">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">{server.name}</span>
            <StateBadge server={server} />
            {server.plugin && <span className="text-xs text-neutral-500">from plugin {server.plugin}</span>}
          </div>
          <p className="mt-0.5 truncate font-mono text-xs text-neutral-500">
            {server.transport === "stdio"
              ? [server.config.command, ...(server.config.args || [])].join(" ")
              : `${server.transport.toUpperCase()} ${server.config.url}`}
          </p>
        </div>
        <div className="flex gap-1">
          {server.tools.length > 0 && (
            <button
              className="rounded-full px-3 py-1 text-xs text-neutral-600 hover:bg-neutral-100 dark:text-neutral-300 dark:hover:bg-neutral-800"
              onClick={() => setExpanded(expanded === server.key ? null : server.key)}
            >
              {expanded === server.key ? "Hide tools" : "Tools"}
            </button>
          )}
          {removable && (
            <>
              <button
                disabled={busy !== null}
                className="rounded-full px-3 py-1 text-xs text-neutral-600 hover:bg-neutral-100 disabled:opacity-50 dark:text-neutral-300 dark:hover:bg-neutral-800"
                onClick={() => void act(server, server.state === "disabled" ? "enable" : "restart")}
              >
                {busy === server.key + "restart" || busy === server.key + "enable"
                  ? "Starting…"
                  : server.state === "disabled"
                    ? "Enable"
                    : "Restart"}
              </button>
              {server.state !== "disabled" && (
                <button
                  disabled={busy !== null}
                  className="rounded-full px-3 py-1 text-xs text-neutral-600 hover:bg-neutral-100 disabled:opacity-50 dark:text-neutral-300 dark:hover:bg-neutral-800"
                  onClick={() => void act(server, "disable")}
                >
                  Disable
                </button>
              )}
              <button
                disabled={busy !== null}
                className="rounded-full px-3 py-1 text-xs text-red-600 hover:bg-red-50 disabled:opacity-50 dark:hover:bg-red-950/40"
                onClick={() => {
                  if (window.confirm(`Remove the MCP server ${server.name}?`)) void act(server, "delete");
                }}
              >
                Remove
              </button>
            </>
          )}
        </div>
      </div>
      {server.error && (
        <p className="mt-2 whitespace-pre-wrap break-words rounded-lg bg-red-50 px-3 py-2 font-mono text-xs text-red-800 dark:bg-red-950/40 dark:text-red-300">
          {server.error}
        </p>
      )}
      {expanded === server.key && (
        <ul className="mt-3 space-y-2 border-t border-neutral-200 pt-3 dark:border-neutral-700">
          {server.tools.map((tool) => (
            <li key={tool.name}>
              <p className="font-mono text-xs font-medium">{tool.name}</p>
              {tool.description && (
                <p className="line-clamp-2 text-xs text-neutral-500">{tool.description}</p>
              )}
            </li>
          ))}
        </ul>
      )}
    </li>
  );

  return (
    <div className="max-w-3xl">
      <h3 className="text-xl font-medium">MCP servers</h3>
      <p className="mt-1 text-sm leading-6 text-neutral-600 dark:text-neutral-300">
        Connect any Model Context Protocol server: a local command (stdio) or a remote URL
        (streamable HTTP or SSE). Its tools are offered to tool-capable models in chat. Type
        @name in chat to use only that server for a turn.
      </p>
      {configPath && (
        <p className="mt-1 text-xs text-neutral-500">
          Saved in <span className="font-mono">{configPath}</span>, the same mcpServers format Claude
          and Cursor use.
        </p>
      )}

      {error && (
        <p className="mt-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950/40 dark:text-red-300">
          {error}
        </p>
      )}

      <div className="mt-6 rounded-2xl bg-neutral-100 px-5 py-4 dark:bg-neutral-800">
        <div className="flex items-center justify-between">
          <p className="text-sm font-medium">Add a server</p>
          <div className="flex gap-1">
            <button className={modeClass("stdio")} onClick={() => setMode("stdio")}>
              Command
            </button>
            <button className={modeClass("http")} onClick={() => setMode("http")}>
              URL
            </button>
            <button className={modeClass("json")} onClick={() => setMode("json")}>
              Paste JSON
            </button>
          </div>
        </div>
        <div className="mt-3 space-y-2">
          {mode !== "json" && (
            <input className={input} placeholder="Name, for example memory" value={name} onChange={(e) => setName(e.target.value)} />
          )}
          {mode === "stdio" && (
            <>
              <input
                className={`${input} font-mono`}
                placeholder="npx -y @modelcontextprotocol/server-memory"
                value={command}
                onChange={(e) => setCommand(e.target.value)}
              />
              <textarea
                className={`${input} font-mono`}
                rows={2}
                placeholder={"Environment, one per line (optional)\nAPI_KEY=..."}
                value={env}
                onChange={(e) => setEnv(e.target.value)}
              />
            </>
          )}
          {mode === "http" && (
            <>
              <input
                className={`${input} font-mono`}
                placeholder="https://example.com/mcp"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
              />
              <textarea
                className={`${input} font-mono`}
                rows={2}
                placeholder={"Headers, one per line (optional)\nAuthorization: Bearer ..."}
                value={headers}
                onChange={(e) => setHeaders(e.target.value)}
              />
            </>
          )}
          {mode === "json" && (
            <textarea
              className={`${input} font-mono`}
              rows={8}
              placeholder={EXAMPLE_JSON}
              value={json}
              onChange={(e) => setJson(e.target.value)}
            />
          )}
          {formError && <p className="text-sm text-red-700 dark:text-red-400">{formError}</p>}
          <button
            disabled={busy !== null}
            onClick={() => void add()}
            className="rounded-full bg-black px-4 py-2 text-sm text-white disabled:opacity-50 dark:bg-white dark:text-black"
          >
            {busy === "add" ? "Connecting…" : "Add and connect"}
          </button>
          <p className="text-xs text-neutral-500">
            The first start of an npx or uvx server can take a minute while it downloads.
          </p>
        </div>
      </div>

      <h4 className="mt-8 text-base font-medium">Your servers</h4>
      {loading && <p className="mt-2 text-sm text-neutral-500">Loading…</p>}
      {!loading && userServers.length === 0 && (
        <p className="mt-2 text-sm text-neutral-500">No MCP servers yet.</p>
      )}
      <ul className="mt-3 space-y-2">{userServers.map((s) => renderServer(s, true))}</ul>

      {pluginServers.length > 0 && (
        <>
          <h4 className="mt-8 text-base font-medium">From enabled plugins</h4>
          <ul className="mt-3 space-y-2">{pluginServers.map((s) => renderServer(s, false))}</ul>
        </>
      )}
    </div>
  );
}
