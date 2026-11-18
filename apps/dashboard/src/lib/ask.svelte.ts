/**
 * One question to the assistant about something a page shows: the daemon
 * reads the material itself, and the answer streams in as text.
 */
import { api, call, type AskAnswer, type AskRequest } from "./api/client.ts";
import { readEvents } from "./sse.ts";

/** What a question is about; the question itself is asked separately. */
export type AskSubject = Omit<AskRequest, "question">;

export class AskRun {
  /** The answer's text so far. */
  text = $state("");
  /** The model's reasoning so far, from a server that sends it. */
  thinking = $state("");
  answer = $state<AskAnswer | null>(null);
  error = $state("");
  running = $state(false);
  #abort: AbortController | undefined;

  async ask(subject: AskSubject, question = "") {
    this.stop();
    this.text = "";
    this.thinking = "";
    this.answer = null;
    this.error = "";
    this.running = true;
    const abort = new AbortController();
    this.#abort = abort;
    try {
      const stream = await call(
        api.POST("/ask", {
          body: { ...subject, question: question.trim() || undefined },
          parseAs: "stream",
          signal: abort.signal,
        }),
      );
      await readEvents(stream, (event, data) => {
        const v = JSON.parse(data);
        if (event === "text") this.text += v.text;
        else if (event === "thinking") this.thinking += v.text;
        else if (event === "error") this.error = v.message;
        else if (event === "answer") this.answer = v as AskAnswer;
      });
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

  stop() {
    this.#abort?.abort();
    this.running = false;
  }
}
