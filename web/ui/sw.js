// The service worker. For now it only makes the app installable on a phone:
// it caches nothing, and every request goes to the network as before.
// Working offline is a later step (OFFLINE.md).
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));
self.addEventListener('fetch', () => {});
