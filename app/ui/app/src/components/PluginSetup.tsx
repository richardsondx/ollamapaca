import { useEffect, useRef, useState } from "react";
import { API_BASE } from "@/lib/config";

// Setup for plugins backed by an MCP server: the settings a plugin
// asks for (API keys, OAuth client IDs), the connection status, and
// Connect / Disconnect for servers that sign in with OAuth.

export type PluginSetting = {
  name: string;
  label: string;
  help?: string;
  url?: string;
  secret?: boolean;
  required?: boolean;
};

export type McpStatus = {
  key: string;
  name: string;
  transport: string;
  state: "idle" | "starting" | "ready" | "error" | "disabled" | "needs_auth" | "needs_setup";
  error?: string;
  tools: { name: string; description?: string }[];
  signedIn?: boolean;
};

export type SetupPlugin = {
  name: string;
  enabled: boolean;
  homepage?: string;
  settings?: PluginSetting[];
  settingsSet?: Record<string, boolean>;
  settingsValues?: Record<string, string>;
  needsSetup?: boolean;
  mcp?: McpStatus[];
};

// pluginStatusLabel is the short status shown under a plugin's name.
export function pluginStatusLabel(p: SetupPlugin & { tools: unknown[] }): string {
  if (!p.enabled) return p.needsSetup ? "Not enabled · needs setup" : "Not enabled";
  if (p.needsSetup) return "Enabled · needs setup";
  const states = (p.mcp || []).map((m) => m.state);
  if (states.includes("needs_auth")) return "Enabled · sign in needed";
  if (states.includes("error")) return "Enabled · error";
  if (states.includes("starting")) return "Enabled · connecting";
  return `Enabled · ${p.tools.length} tools`;
}

async function readError(response: Response): Promise<string> {
  const text = await response.text();
  try {
    return JSON.parse(text).error || text;
  } catch {
    return text || `HTTP ${response.status}`;
  }
}

export function PluginSetup({ plugin, onChange }: { plugin: SetupPlugin; onChange: () => void }) {
  const settings = plugin.settings || [];
  const servers = plugin.mcp || [];
  const remote = servers.find((s) => s.transport !== "stdio");
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const latest = useRef(plugin);
  latest.current = plugin;

  useEffect(() => {
    setValues({ ...(plugin.settingsValues || {}) });
    setMessage(null);
    setError(null);
  }, [plugin.name, plugin.settingsValues]);

  if (settings.length === 0 && servers.length === 0) return null;

  const save = async () => {
    setBusy("save");
    setError(null);
    try {
      const response = await fetch(`${API_BASE}/api/plugins/${plugin.name}/settings`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ values }),
      });
      if (!response.ok) throw new Error(await readError(response));
      setMessage("Saved.");
      // Secret fields are never sent back, so clear them after saving.
      setValues((v) => {
        const next = { ...v };
        for (const s of settings) if (s.secret) delete next[s.name];
        return next;
      });
      onChange();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save");
    } finally {
      setBusy(null);
    }
  };

  const account = async (action: "signin" | "signout") => {
    setBusy(action);
    setError(null);
    setMessage(null);
    try {
      const response = await fetch(`${API_BASE}/api/plugins/${plugin.name}/${action}`, { method: "POST" });
      if (!response.ok) throw new Error(await readError(response));
      if (action === "signin") {
        const data = await response.json();
        setMessage(
          data.opened
            ? "Finish signing in in your browser. This page updates when you are done."
            : "Open this link to sign in: " + data.authUrl,
        );
        // Poll until the server reports it is signed in (3 minutes max).
        for (let i = 0; i < 60; i++) {
          await new Promise((r) => setTimeout(r, 3000));
          onChange();
          const now = (latest.current.mcp || []).find((s) => s.transport !== "stdio");
          if (now?.signedIn && now.state === "ready") {
            setMessage("Connected.");
            break;
          }
        }
      } else {
        onChange();
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not " + action);
    } finally {
      setBusy(null);
    }
  };

  const input =
    "w-full rounded-lg border border-neutral-200 bg-white px-3 py-2 text-sm outline-none focus:border-neutral-400 dark:border-neutral-700 dark:bg-neutral-900";

  return (
    <div className="mt-6 rounded-2xl border border-neutral-200 px-5 py-4 dark:border-neutral-700">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm font-medium">Connection</p>
        {plugin.homepage && (
          <a href={plugin.homepage} target="_blank" rel="noreferrer" className="text-xs text-neutral-500 underline">
            {plugin.homepage.replace(/^https?:\/\//, "")}
          </a>
        )}
      </div>

      {servers.length > 0 && (
        <ul className="mt-2 space-y-1">
          {servers.map((s) => (
            <li key={s.key} className="text-sm text-neutral-600 dark:text-neutral-300">
              {s.state === "ready" && <>Connected · {s.tools.length} tools{s.signedIn ? " · signed in" : ""}</>}
              {s.state === "starting" && <>Connecting…</>}
              {s.state === "idle" && <>Connects when you use it.</>}
              {s.state === "needs_auth" && <>Sign in to use this plugin.</>}
              {s.state === "needs_setup" && <>{s.error}</>}
              {s.state === "error" && <span className="text-red-700 dark:text-red-400">{s.error}</span>}
            </li>
          ))}
        </ul>
      )}
      {!plugin.enabled && servers.length === 0 && (
        <p className="mt-2 text-sm text-neutral-500">Enable the plugin to connect it.</p>
      )}

      {settings.length > 0 && (
        <div className="mt-4 space-y-3">
          {settings.map((s) => (
            <label key={s.name} className="block">
              <span className="text-sm">
                {s.label}
                {s.required ? "" : " (optional)"}
                {plugin.settingsSet?.[s.name] && <span className="ml-2 text-xs text-emerald-700 dark:text-emerald-400">saved</span>}
              </span>
              <input
                className={`${input} mt-1 font-mono`}
                type={s.secret ? "password" : "text"}
                autoComplete="off"
                placeholder={s.secret && plugin.settingsSet?.[s.name] ? "•••••••• (leave empty to keep)" : s.name}
                value={values[s.name] ?? ""}
                onChange={(e) => setValues({ ...values, [s.name]: e.target.value })}
              />
              {(s.help || s.url) && (
                <span className="mt-1 block text-xs text-neutral-500">
                  {s.help}{" "}
                  {s.url && (
                    <a href={s.url} target="_blank" rel="noreferrer" className="underline">
                      Get one
                    </a>
                  )}
                </span>
              )}
            </label>
          ))}
          <button
            disabled={busy !== null}
            onClick={() => void save()}
            className="rounded-full bg-black px-4 py-2 text-sm text-white disabled:opacity-50 dark:bg-white dark:text-black"
          >
            {busy === "save" ? "Saving…" : "Save"}
          </button>
        </div>
      )}

      {plugin.enabled && remote && (remote.state === "needs_auth" || remote.signedIn) && (
        <div className="mt-4 flex gap-2">
          {!remote.signedIn ? (
            <button
              disabled={busy !== null}
              onClick={() => void account("signin")}
              className="rounded-full bg-black px-4 py-2 text-sm text-white disabled:opacity-50 dark:bg-white dark:text-black"
            >
              {busy === "signin" ? "Waiting for sign-in…" : "Connect"}
            </button>
          ) : (
            <button
              disabled={busy !== null}
              onClick={() => void account("signout")}
              className="rounded-full border border-neutral-300 px-4 py-2 text-sm disabled:opacity-50 dark:border-neutral-600"
            >
              Disconnect
            </button>
          )}
        </div>
      )}

      {message && <p className="mt-3 break-words text-sm text-neutral-600 dark:text-neutral-300">{message}</p>}
      {error && <p className="mt-3 break-words text-sm text-red-700 dark:text-red-400">{error}</p>}
    </div>
  );
}
