// Dashboard service worker: makes the app installable. No offline caching:
// every request goes to the network as if this worker were not here.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));
