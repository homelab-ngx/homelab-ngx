import { redirect } from "@sveltejs/kit";

// The page moved; stored notifications and bookmarks still point here.
export const load = () => redirect(308, "/settings/updates");
