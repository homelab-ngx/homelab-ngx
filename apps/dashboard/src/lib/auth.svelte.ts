/**
 * The dashboard's sign-in state. The daemon gates every call behind one
 * password; any answer that says the session is gone sends the page back to
 * the sign-in screen.
 */
import { api, call, onSignedOut, ProblemError } from "#lib/api/client.ts";

/** checking: not known yet; setup: no password yet; login: signed out; open: signed in, or no sign-in on this host. */
export type Gate = "checking" | "setup" | "login" | "open";

class Session {
  gate = $state<Gate>("checking");

  async check() {
    try {
      const s = await call(api.GET("/auth"));
      this.gate = !s.password_set ? "setup" : s.signed_in ? "open" : "login";
    } catch (e) {
      // The installer stick has no sign-in and no /auth (404). An unreachable
      // daemon shows on the pages themselves.
      if (this.gate === "checking" || (e instanceof ProblemError && e.status === 404)) this.gate = "open";
    }
  }

  signedIn() {
    this.gate = "open";
  }

  async signOut() {
    await call(api.POST("/auth/logout"));
    this.gate = "login";
  }
}

export const session = new Session();
onSignedOut((code) => (session.gate = code === "setup_required" ? "setup" : "login"));
