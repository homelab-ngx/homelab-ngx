/**
 * A stack shell: root's login shell in a running stack, over a WebSocket to
 * the daemon (GET /api/stacks/{name}/shell). Binary frames carry the
 * terminal's bytes both ways; a text frame from here is a resize. The
 * shell window (routes/shell/[name]) puts a terminal on it.
 */

export type ShellStatus = "connecting" | "open" | "closed";

/** The shell window's address, which the stack page opens as a popup. */
export function shellPath(stack: string): string {
  return `/shell/${encodeURIComponent(stack)}`;
}

/** Opens the stack's shell window, or brings it forward when it is open. */
export function openShellWindow(stack: string): void {
  window.open(shellPath(stack), `shell-${stack}`, "popup,width=960,height=600");
}

export function resizeFrame(cols: number, rows: number): string {
  return JSON.stringify({ type: "resize", cols, rows });
}

/** The terminal's font: the dashboard's mono face, as DESIGN.md sets it. */
export const MONO = '"Azeret Mono Variable", ui-monospace, monospace';

/**
 * The terminal's colours from the dashboard's tokens (token name to value).
 * ANSI colours map as the log pane maps them (ansi.ts): standard and bright
 * share a token, and the theme picks the contrast.
 */
export function terminalTheme(token: (name: string) => string) {
  const [muted, bad, ok, warn, accent, core, fg] = ["muted", "bad", "ok", "warn", "accent", "core", "fg"].map(token);
  const ansi = { black: muted, red: bad, green: ok, yellow: warn, blue: accent, magenta: core, cyan: accent, white: fg };
  return {
    background: token("sunken"),
    foreground: fg,
    cursor: fg,
    cursorAccent: token("sunken"),
    selectionBackground: token("accent-soft"),
    selectionForeground: fg,
    ...ansi,
    brightBlack: muted,
    brightRed: bad,
    brightGreen: ok,
    brightYellow: warn,
    brightBlue: accent,
    brightMagenta: core,
    brightCyan: accent,
    brightWhite: fg,
  };
}

/** The connection was refused before it opened. */
export const REFUSED = "The host refused or dropped the connection.";

/**
 * Why a refused shell was refused, from the stack's state: the daemon's
 * answer to the upgrade never reaches the page. null: the state does not say.
 */
export function refusal(stack: { running: boolean; frozen: boolean } | null): string | null {
  if (!stack) return null;
  if (!stack.running) return "The stack is not running.";
  if (stack.frozen) return "The stack is frozen.";
  return null;
}

type Keys =Pick<KeyboardEvent, "key" | "code" | "metaKey" | "ctrlKey" | "shiftKey" | "altKey">;

/**
 * Whether a key copies the selection: Cmd+C on a Mac, Ctrl+Shift+C
 * elsewhere. Plain Ctrl+C always goes to the shell as an interrupt. Paste
 * needs no key here: Cmd+V and Ctrl+Shift+V reach the browser's paste.
 */
export function isCopyKey(e: Keys, mac: boolean): boolean {
  if (e.code !== "KeyC" || e.altKey) return false;
  return mac ? e.metaKey && !e.ctrlKey && !e.shiftKey : e.ctrlKey && e.shiftKey && !e.metaKey;
}

/** Puts text on the clipboard; over plain HTTP the Clipboard API is missing. */
export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.append(area);
  area.select();
  document.execCommand("copy");
  area.remove();
}

/** One connection to a stack's shell. A closed one is replaced, not reopened. */
export class ShellSession {
  status: ShellStatus = "connecting";
  /** Why it closed. */
  reason = "";
  readonly #ws: WebSocket;

  constructor(stack: string, handlers: { output: (bytes: Uint8Array) => void; opened: () => void; closed: (reason: string) => void }) {
    const scheme = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${scheme}://${location.host}/api/stacks/${encodeURIComponent(stack)}/shell`);
    ws.binaryType = "arraybuffer";
    this.#ws = ws;
    ws.onopen = () => {
      this.status = "open";
      handlers.opened();
    };
    ws.onmessage = (e) => {
      if (e.data instanceof ArrayBuffer) handlers.output(new Uint8Array(e.data));
    };
    ws.onclose = (e) => {
      // A refusal (stopped, frozen, too many shells) comes before the
      // upgrade, so the browser only sees 1006 and no reason.
      this.reason = e.reason || (e.code === 1006 ? REFUSED : "Closed.");
      this.status = "closed";
      handlers.closed(this.reason);
    };
  }

  send(data: string): void {
    if (this.status === "open") this.#ws.send(new TextEncoder().encode(data));
  }

  resize(cols: number, rows: number): void {
    if (this.status === "open") this.#ws.send(resizeFrame(cols, rows));
  }

  close(): void {
    this.#ws.close();
  }
}
