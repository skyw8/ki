// A dropped connection surfaces from `fetch` as a TypeError ("Failed to fetch",
// "Load failed", "NetworkError when attempting to fetch resource."). Unlike an
// HTTP failure (ApiError) it carries no server-side meaning: the push stream
// reconnects on its own and the next refresh succeeds. Background syncs must
// therefore not echo the browser's message as an error toast — a phone that
// wakes from sleep (or changes network) would otherwise greet the user with a
// raw "network error" every time it comes back.
export function isNetworkError(err: unknown): boolean {
  return err instanceof TypeError
}
