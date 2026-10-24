export type MarkdownTarget =
  | { kind: 'external'; url: string }
  | { kind: 'anchor'; hash: string }
  | { kind: 'local'; path: string; line?: number; column?: number }
  | { kind: 'invalid' }

const SCHEME = /^[a-zA-Z][a-zA-Z0-9+.-]*:/
const DRIVE = /^([a-zA-Z]):[\\/]/
const EXTERNAL_SCHEMES = new Set(['http', 'https', 'mailto', 'tel'])

export function isAbsoluteLocalPath(path: string): boolean {
  return path.startsWith('/') || path.startsWith('\\') || /^[a-zA-Z]:[\\/]/.test(path)
}

export function normalizeLocalPath(path: string): string {
  const driveMatch = DRIVE.exec(path)
  const drive = driveMatch ? `${driveMatch[1].toUpperCase()}:/` : ''
  const rest = (driveMatch ? path.slice(driveMatch[0].length) : path).replace(/\\/g, '/')
  const rooted = rest.startsWith('/')
  const segments: string[] = []
  for (const segment of rest.split('/')) {
    if (!segment || segment === '.') continue
    if (segment === '..') {
      segments.pop()
      continue
    }
    segments.push(segment)
  }
  if (drive) return `${drive}${segments.join('/')}`
  return `${rooted ? '/' : ''}${segments.join('/')}`
}

export function resolveLocalPath(href: string, cwd: string): string {
  if (isAbsoluteLocalPath(href)) return normalizeLocalPath(href)
  const base = cwd ? normalizeLocalPath(cwd) : ''
  if (!base) return normalizeLocalPath(href)
  return normalizeLocalPath(`${base.replace(/\/+$/, '')}/${href}`)
}

function decodePath(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

export function splitLocation(path: string): { path: string; line?: number; column?: number } {
  const match = /^(.*?):(\d+)(?::(\d+))?$/.exec(path)
  if (!match || !match[1] || /^[a-zA-Z]$/.test(match[1])) return { path }
  return { path: match[1], line: Number(match[2]), column: match[3] ? Number(match[3]) : undefined }
}

export function classifyMarkdownHref(href: string, cwd: string): MarkdownTarget {
  const raw = href.trim()
  if (!raw) return { kind: 'invalid' }
  if (raw.startsWith('#')) return { kind: 'anchor', hash: raw }
  if (raw.startsWith('//')) return { kind: 'external', url: raw }
  const scheme = SCHEME.exec(raw)?.[0] ?? ''
  const schemeName = scheme.slice(0, -1).toLowerCase()
  if (scheme && !DRIVE.test(raw)) {
    if (EXTERNAL_SCHEMES.has(schemeName)) return { kind: 'external', url: raw }
    if (!schemeName.includes('.')) return { kind: 'invalid' }
  }
  const cut = raw.search(/[?#]/)
  const location = cut >= 0 ? raw.slice(0, cut) : raw
  const resolved = resolveLocalPath(decodePath(location), cwd)
  const { path, line, column } = splitLocation(resolved)
  if (!path) return { kind: 'invalid' }
  return { kind: 'local', path, line, column }
}

export function isMarkdownImagePath(path: string): boolean {
  return /\.(png|jpe?g|gif|webp|avif|svg|bmp)$/i.test(path)
}
