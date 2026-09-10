// PlantUML has no client-side renderer, so diagrams are produced by a PlantUML
// server: the source is deflate-compressed and re-encoded with PlantUML's URL
// alphabet, then fetched as `/svg/<code>` or `/png/<code>`. The public server is
// the default; a browser can point elsewhere (self-hosted / intranet) through
// localStorage so the same build works offline.
const DEFAULT_SERVER = 'https://www.plantuml.com/plantuml'

const OVERRIDE_KEY = 'ki.plantumlServer'

export function plantumlServer(): string {
  try {
    return (localStorage.getItem(OVERRIDE_KEY) || DEFAULT_SERVER).replace(/\/+$/, '')
  } catch {
    return DEFAULT_SERVER
  }
}

// encode6bit / append3bytes implement PlantUML's base64 variant, which is not
// standard base64: index 62/63 map to '-' and '_' instead of '+' and '/'.
function encode6bit(b: number): string {
  if (b < 10) return String.fromCharCode(48 + b)
  b -= 10
  if (b < 26) return String.fromCharCode(65 + b)
  b -= 26
  if (b < 26) return String.fromCharCode(97 + b)
  b -= 26
  if (b === 0) return '-'
  if (b === 1) return '_'
  return '?'
}

function append3bytes(b1: number, b2: number, b3: number): string {
  const c1 = b1 >> 2
  const c2 = ((b1 & 0x3) << 4) | (b2 >> 4)
  const c3 = ((b2 & 0xf) << 2) | (b3 >> 6)
  const c4 = b3 & 0x3f
  return encode6bit(c1 & 0x3f) + encode6bit(c2 & 0x3f) + encode6bit(c3 & 0x3f) + encode6bit(c4 & 0x3f)
}

function encode64(bytes: Uint8Array): string {
  let out = ''
  for (let i = 0; i < bytes.length; i += 3) {
    if (i + 2 === bytes.length) out += append3bytes(bytes[i], bytes[i + 1], 0)
    else if (i + 1 === bytes.length) out += append3bytes(bytes[i], 0, 0)
    else out += append3bytes(bytes[i], bytes[i + 1], bytes[i + 2])
  }
  return out
}

async function deflateRaw(input: Uint8Array): Promise<Uint8Array> {
  // TextEncoder always returns an ArrayBuffer-backed view, but TS types the
  // buffer as ArrayBufferLike (which includes SharedArrayBuffer).
  const stream = new Blob([input as BlobPart]).stream().pipeThrough(new CompressionStream('deflate-raw'))
  return new Uint8Array(await new Response(stream).arrayBuffer())
}

// plantumlUrl returns the server URL that renders source in the given format.
export async function plantumlUrl(source: string, format: 'svg' | 'png'): Promise<string> {
  const compressed = await deflateRaw(new TextEncoder().encode(source))
  return `${plantumlServer()}/${format}/${encode64(compressed)}`
}
