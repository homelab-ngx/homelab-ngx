/**
 * Whether the Storage page shows everything (disks, partitions, spare disks,
 * pool actions) or only the space used. Remembered on this device; off at
 * first.
 */
const KEY = "homelab-storage-advanced";

function stored(): boolean {
  try {
    return localStorage.getItem(KEY) === "1";
  } catch {
    return false;
  }
}

export const storageView = $state({ advanced: stored() });

export function setAdvanced(on: boolean) {
  storageView.advanced = on;
  try {
    localStorage.setItem(KEY, on ? "1" : "0");
  } catch {
    // Private mode: the choice still holds for this visit.
  }
}
