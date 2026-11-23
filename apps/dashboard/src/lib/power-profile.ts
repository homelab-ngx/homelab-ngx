/** The power profile: the pure logic behind System Settings › Power profile. */
import { api, call, type Schema } from "./api/client.ts";
import type { Tone } from "./components/Pill.svelte";

export type PowerProfileStatus = Schema<"PowerProfileStatus">;
export type PowerProfile = PowerProfileStatus["profile"];
export type PowerBlocker = Schema<"PowerBlocker">;
export type PowerTunable = Schema<"PowerTunable">;

export const powerProfileKey = ["daemon", "system", "power-profile"] as const;
export const fetchPowerProfile = () => call<PowerProfileStatus>(api.GET("/system/power-profile"));

export const PROFILES: { value: PowerProfile; label: string; note: string }[] = [
  { value: "power-saver", label: "Power saver", note: "every link and device sleeps" },
  { value: "balanced", label: "Balanced", note: "safe savings" },
  { value: "performance", label: "Performance", note: "highest clocks, links awake" },
];

export const profileLabel = (p: PowerProfile) => PROFILES.find((x) => x.value === p)?.label ?? p;

/** Watts to one decimal below 10 W, whole above. */
export function watts(w: number | null | undefined): string {
  if (w === null || w === undefined || !Number.isFinite(w)) return "";
  return `${w < 10 ? w.toFixed(1) : Math.round(w)} W`;
}

/** A share of time as a percentage; a sliver shows as "<1%" rather than 0. */
export function share(v: number): string {
  if (v > 0 && v < 0.005) return "<1%";
  return `${Math.round(v * 100)}%`;
}

/** What the profile did, as one mark and word. */
export function appliedState(st: PowerProfileStatus): { tone: Tone; word: string } {
  if (st.read_only) return { tone: "idle", word: "read only" };
  if (st.tunables.length === 0) return { tone: "info", word: "applying" };
  if (st.tunables.some((t) => t.state === "refused" || t.state === "partial")) return { tone: "warn", word: "partial" };
  return { tone: "ok", word: "applied" };
}

/** A tunable's name for a row. */
export const TUNABLES: Record<PowerTunable["name"], string> = {
  governor: "CPU governor",
  epp: "Energy preference",
  aspm_policy: "PCIe ASPM policy",
  aspm_links: "PCIe link sleep",
  pci_runtime_pm: "PCI runtime PM",
  usb_autosuspend: "USB autosuspend",
  sata_alpm: "SATA link power",
  nmi_watchdog: "NMI watchdog",
  dirty_writeback: "Disk write-back",
};

/** A tunable's state as one mark and word, and its value line. */
export function tunableRow(t: PowerTunable): { tone: Tone; word: string; value: string } {
  // Switches are written as 1 and 0.
  const shown = t.name === "aspm_links" || t.name === "nmi_watchdog" ? ({ "1": "on", "0": "off" }[t.value] ?? t.value) : t.value;
  const value = t.refused > 0 ? `${t.refused} of ${t.files} refused${t.detail ? `: ${t.detail}` : ""}` : shown || t.detail || "as booted";
  switch (t.state) {
    case "set":
      return { tone: "ok", word: "set", value };
    case "boot":
      return { tone: "idle", word: "default", value };
    case "partial":
      return { tone: "warn", word: "partial", value };
    case "refused":
      return { tone: "bad", word: "refused", value };
  }
  return { tone: "idle", word: "none", value: "not on this host" };
}

const BLOCKS: Record<PowerBlocker["kind"], string> = {
  aspm: "PCIe link L1 off",
  runtime_pm: "runtime PM on",
  usb_autosuspend: "autosuspend off",
};

/** A blocker's name and what keeps it awake. */
export function blockerRow(b: PowerBlocker): { name: string; detail: string } {
  const name = b.name || b.driver || b.id;
  return { name, detail: [b.device, b.id, BLOCKS[b.kind], `${profileLabel(b.fix)} turns it off`].join(" · ") };
}
