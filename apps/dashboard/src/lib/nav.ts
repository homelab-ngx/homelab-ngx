/** The dashboard's top level. */
export interface NavItem {
  label: string;
  href: string;
}

export const NAV: NavItem[] = [
  { label: "Overview", href: "/" },
  { label: "Stacks", href: "/stacks" },
  { label: "Storage", href: "/storage" },
  { label: "Backups", href: "/backups" },
  { label: "Notifications", href: "/notifications" },
];

export const SETTINGS: NavItem = { label: "System Settings", href: "/settings" };

/** The nav item a path belongs to. */
export function current(pathname: string): string {
  if (pathname === "/") return "/";
  return [...NAV, SETTINGS].find((n) => n.href !== "/" && pathname.startsWith(n.href))?.href ?? "";
}

/** The pages that hold an editor take the whole width: a draft, and a stack's source code. */
export const wide = (pathname: string) => /^\/stacks\/(drafts\/[^/]+|[^/]+\/source)\/?$/.test(pathname);
