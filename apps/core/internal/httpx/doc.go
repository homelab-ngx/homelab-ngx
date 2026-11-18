// Package httpx is the HTTP plumbing shared by every feature package:
// the huma APIs and operation scopes, RFC 9457
// problem+json errors, the request-logging middleware and the servers.
//
// Feature packages register their operations on huma with Register, at a
// Scope. Handlers never write error bodies themselves: they return a
// *Problem (or an error wrapping one) and huma renders it; any other error
// is logged and answered with a generic 500.
package httpx
