/**
 * One question to the stack editor's assistant: the answer streams in as
 * text, then arrives read (notes and proposed files). Nothing is applied
 * here; the editor applies what the operator picks.
 */
import { api, call, type DraftAnswer, type DraftChange } from "./api/client.ts";
import type { Proposal } from "./drafts.ts";
import { readEvents } from "./sse.ts";

export type AssistKind = "draft" | "review" | "fix" | "ask";

export class AssistRun {
  /** The answer's text so far, file blocks included. */
  text = $state("");
  /** The model's reasoning so far, from a server that sends it. */
  thinking = $state("");
  answer = $state<DraftAnswer | null>(null);
  /** The proposal not yet applied or discarded, by path. */
  proposal = $state<Proposal>({});
  error = $state("");
  running = $state(false);
  #abort: AbortController | undefined;

  /** Asks about the draft name with the editor's unsaved changes and the file it shows open. */
  async ask(name: string, kind: AssistKind, text: string, changes: DraftChange[], open: string) {
    this.stop();
    this.text = "";
    this.thinking = "";
    this.answer = null;
    this.proposal = {};
    this.error = "";
    this.running = true;
    const abort = new AbortController();
    this.#abort = abort;
    try {
      const stream = await call(
        api.POST("/drafts/{name}/assist", {
          params: { path: { name } },
          body: { kind, text: text.trim() || undefined, changes, open },
          parseAs: "stream",
          signal: abort.signal,
        }),
      );
      await readEvents(stream, (event, data) => this.#event(event, data));
      if (!this.answer && !this.error) this.error = "The answer stopped before it was complete. Ask again.";
    } catch (err) {
      if (!abort.signal.aborted) this.error = err instanceof Error ? err.message : String(err);
    } finally {
      if (this.#abort === abort) {
        this.running = false;
        this.#abort = undefined;
      }
    }
  }

  #event(event: string, data: string) {
    const v = JSON.parse(data);
    if (event === "text") this.text += v.text;
    else if (event === "thinking") this.thinking += v.text;
    else if (event === "error") this.error = v.message;
    else if (event === "answer") {
      this.answer = v as DraftAnswer;
      this.proposal = Object.fromEntries((v as DraftAnswer).files.map((f) => [f.path, f.content]));
    }
  }

  /** Forget the proposal for one file (applied or discarded). */
  settle(path: string) {
    delete this.proposal[path];
  }

  stop() {
    this.#abort?.abort();
    this.running = false;
  }
}
