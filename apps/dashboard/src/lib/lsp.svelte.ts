/**
 * The stack editor's Nix language server: nixd on the host, reached over a
 * WebSocket to the daemon (GET /api/drafts/{name}/lsp), which starts it and
 * points it at the draft's nixpkgs. One per open editor; it stops when the
 * editor closes.
 */
import { languageServerExtensions, LSPClient, type Transport } from "@codemirror/lsp-client";
import type { Extension } from "@codemirror/state";

export type LspStatus = "starting" | "ready" | "off";

/** Markdown from the server becomes HTML: keep text and plain markup only. */
function sanitize(html: string): string {
  const doc = new DOMParser().parseFromString(html, "text/html");
  doc.querySelectorAll("script, style, iframe, object, embed, link, meta, form").forEach((n) => n.remove());
  doc.querySelectorAll("*").forEach((el) => {
    for (const a of [...el.attributes]) {
      if (a.name.startsWith("on") || (a.name === "href" && !/^(https?:|#)/i.test(a.value)) || a.name === "src") el.removeAttribute(a.name);
    }
  });
  return doc.body.innerHTML;
}

export class NixLanguageServer {
  status = $state<LspStatus>("starting");
  /** Why it is off. */
  reason = $state("");
  readonly #draft: string;
  #client = new LSPClient({ extensions: languageServerExtensions(), timeout: 30_000, sanitizeHTML: sanitize });
  #ws: WebSocket | undefined;
  #closed = false;

  constructor(draft: string) {
    this.#draft = draft;
    const scheme = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${scheme}://${location.host}/api/drafts/${encodeURIComponent(draft)}/lsp`);
    this.#ws = ws;
    const handlers = new Set<(m: string) => void>();
    const transport: Transport = {
      send: (m) => ws.send(m),
      subscribe: (h) => void handlers.add(h),
      unsubscribe: (h) => void handlers.delete(h),
    };
    ws.onmessage = (e) => {
      for (const h of handlers) h(String(e.data));
    };
    ws.onopen = () => {
      this.#client.connect(transport);
      this.#client.initializing.then(
        () => {
          if (!this.#closed) this.status = "ready";
        },
        (err: unknown) => this.#off(err instanceof Error ? err.message : String(err)),
      );
    };
    ws.onclose = (e) => this.#off(e.reason || (e.code === 1006 ? "The host refused or dropped the connection." : "Closed."));
  }

  #off(reason: string) {
    if (this.#closed) return;
    this.status = "off";
    this.reason = reason;
    if (this.#client.connected) this.#client.disconnect();
  }

  /** The editor extension for the Nix file at path in the draft; only once the server is ready. */
  extension(path: string): Extension {
    const uri = `file:///drafts/${encodeURIComponent(this.#draft)}/${path.split("/").map(encodeURIComponent).join("/")}`;
    return this.#client.plugin(uri, "nix");
  }

  close() {
    this.#closed = true;
    if (this.#client.connected) this.#client.disconnect();
    this.#ws?.close();
  }
}
