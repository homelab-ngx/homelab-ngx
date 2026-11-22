/**
 * How the Stacks overview draws its cards: minimal (a strip of service icons)
 * or detailed (a row per service). Remembered on this device.
 */
export type StacksView = "minimal" | "detailed";

const KEY = "homelab-stacks-view";

function stored(): StacksView {
  try {
    return localStorage.getItem(KEY) === "detailed" ? "detailed" : "minimal";
  } catch {
    return "minimal";
  }
}

export const stacksView = $state({ view: stored() });

export function setStacksView(v: StacksView) {
  stacksView.view = v;
  try {
    localStorage.setItem(KEY, v);
  } catch {
    // Private mode: the choice still holds for this visit.
  }
}
