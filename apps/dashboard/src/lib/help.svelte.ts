/**
 * Help on a page: every label that has help unfolds it when clicked, and the
 * header's Help switch unfolds all of them at once. The switch is remembered
 * on this device.
 */
const KEY = "homelab-help";

function stored(): boolean {
  try {
    return localStorage.getItem(KEY) === "1";
  } catch {
    return false;
  }
}

export const help = $state({ all: stored() });

export function toggleHelp() {
  help.all = !help.all;
  try {
    localStorage.setItem(KEY, help.all ? "1" : "0");
  } catch {
    // Private mode: the switch still works for this visit.
  }
}
