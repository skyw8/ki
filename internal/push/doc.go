// Package push delivers Web Push (RFC 8030) completion notifications to the
// browser subscriptions a WebUI client registers.
//
// Why it exists: the WebUI learns that a run finished from its push stream
// (GET /v1/events), which only runs while the page does. A phone that locks its
// screen freezes that page, so the completion is missed until the tab resumes.
// Web Push moves delivery to the browser's push service, which wakes the
// service worker even when no page is running.
//
// Shape:
//
//   - Key is the VAPID application-server key pair (RFC 8292) under
//     {KI_HOME}/vapid.json. Its public half is handed to the browser as the
//     applicationServerKey; its private half signs the JWT on every request.
//   - Store is the {KI_HOME}/push-subscriptions.json registry. A subscription
//     is the endpoint plus the client's P-256 and auth secrets.
//   - Service encrypts each message for one subscription (RFC 8291, aes128gcm)
//     and POSTs it with the VAPID authorization header. Delivery is
//     best-effort: the server drops a notification that arrives while the
//     send queue is full, and prunes a subscription the push service reports
//     as gone (404/410).
//
// The message payload is a JSON run-complete frame; the service worker renders
// the OS notification and suppresses it when a focused ki tab is already
// showing the completion.
package push
