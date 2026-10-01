/** Match the Go tool-name adapter; schemas and UI display use one canonical spelling. */
export function canonicalToolName(name: string): string {
  return name.replace(/([A-Z]+)([A-Z][a-z])/g, '$1_$2').replace(/([a-z0-9])([A-Z])/g, '$1_$2').toLowerCase()
}
