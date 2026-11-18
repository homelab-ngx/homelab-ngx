/**
 * ANSI SGR colour codes in journal lines, as text segments with a theme
 * class. Only the foreground colour and bold are kept; every other escape
 * is dropped. The text stays text: pages render segments, never HTML.
 */
export interface Segment {
  text: string;
  /** Tailwind classes for the segment; "" for plain text. */
  cls: string;
}

// Standard and bright colours share a token: the themes pick contrast.
const colours: Record<number, string> = {
  30: "text-muted",
  31: "text-bad",
  32: "text-ok",
  33: "text-warn",
  34: "text-accent",
  35: "text-core",
  36: "text-accent",
  37: "",
};

const escape = /\x1b\[([0-9;?]*)([A-Za-z])/g;

export function segments(line: string): Segment[] {
  const out: Segment[] = [];
  let colour = "";
  let bold = false;
  let last = 0;
  const push = (text: string) => {
    if (!text) return;
    const cls = [colour, bold ? "font-semibold" : ""].filter(Boolean).join(" ");
    const prev = out.at(-1);
    if (prev && prev.cls === cls) prev.text += text;
    else out.push({ text, cls });
  };
  for (const m of line.matchAll(escape)) {
    push(line.slice(last, m.index));
    last = m.index + m[0].length;
    if (m[2] !== "m") continue;
    const codes = (m[1] || "0").split(";").map(Number);
    for (let i = 0; i < codes.length; i++) {
      const c = codes[i]!;
      if (c === 0) {
        colour = "";
        bold = false;
      } else if (c === 1) bold = true;
      else if (c === 22) bold = false;
      else if (c === 39) colour = "";
      else if (c >= 30 && c <= 37) colour = colours[c]!;
      else if (c >= 90 && c <= 97) colour = colours[c - 60]!;
      // 256-colour and RGB forms: skip their arguments, keep the colour.
      else if (c === 38 || c === 48) i += codes[i + 1] === 5 ? 2 : 4;
    }
  }
  push(line.slice(last));
  return out;
}
