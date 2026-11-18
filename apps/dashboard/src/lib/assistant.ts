/** The assistant's setup in words, for Settings and the stack editor. */
import type { AnswerUsage, AssistantSettings } from "./api/client.ts";

export const PROVIDERS = {
  none: "Off",
  anthropic: "Claude",
  openai: "OpenAI-compatible",
} as const;

/** Models offered for Claude; any other id can be typed. */
export const CLAUDE_MODELS = ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"];

/** "Claude · claude-opus-5-5", or "Off". */
export function assistantLabel(s: AssistantSettings | undefined): string {
  if (!s || s.provider === "none") return PROVIDERS.none;
  return `${PROVIDERS[s.provider]} · ${s.model}`;
}

/** Where an OpenAI-compatible server usually answers. */
export const OPENAI_EXAMPLES = [
  { name: "OpenAI", url: "https://api.openai.com/v1" },
  { name: "Ollama", url: "http://localhost:11434/v1" },
  { name: "LM Studio", url: "http://localhost:1234/v1" },
  { name: "OpenRouter", url: "https://openrouter.ai/api/v1" },
  { name: "OpenCode Go", url: "https://opencode.ai/inference/go/v1" },
];

/** "15 in · 73 out · $0.00004 of plan": tokens, then what was charged or counted. */
export function usageLabel(u: AnswerUsage | undefined): string {
  if (!u || (!u.input_tokens && !u.output_tokens)) return "";
  const parts = [`${u.input_tokens} in`, `${u.output_tokens} out`];
  if (u.cost) parts.push(dollars(u.cost));
  if (u.allowance) parts.push(`${dollars(u.allowance)} of plan`);
  return parts.join(" · ");
}

function dollars(n: number): string {
  return `$${n >= 0.01 ? n.toFixed(2) : n.toPrecision(2)}`;
}

/** What a usage window is called. */
export const LIMIT_WINDOWS: Record<string, string> = { rolling: "5 hours", weekly: "Week", monthly: "Month" };
