export function normalizeMarkdown(src: string): string {
  return src.replace(/\r\n/g, '\n').replace(/\uFF40/g, '`')
}
