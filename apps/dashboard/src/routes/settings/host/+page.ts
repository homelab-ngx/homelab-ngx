import { redirect } from "@sveltejs/kit";

// Host moved into System Settings; old links and bookmarks still point here.
export const load = () => redirect(308, "/settings/system#host");
