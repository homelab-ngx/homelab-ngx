import { redirect } from "@sveltejs/kit";

// The page moved: channels are in Settings, the rest is in the header's tray.
// Stored notifications and bookmarks still point here.
export const load = () => redirect(308, "/settings/notifications");
