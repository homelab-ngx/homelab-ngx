/** The daemon's bounds on a new password, in characters (it counts bytes; the floor holds for ASCII). */
export const MIN_PASSWORD = 10;
export const MAX_PASSWORD = 256;

/** Why a new password cannot be set yet, or "" when it can. */
export function newPasswordProblem(password: string, again: string): string {
  if (password.length < MIN_PASSWORD) return `At least ${MIN_PASSWORD} characters.`;
  if (new TextEncoder().encode(password).length > MAX_PASSWORD) return `At most ${MAX_PASSWORD} characters.`;
  if (again !== password) return "The two passwords differ.";
  return "";
}
