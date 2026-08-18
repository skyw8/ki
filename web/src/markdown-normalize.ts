// Models sometimes emit fullwidth backticks; remend only closes ASCII
// ` runs, so a ｀span｀ would stay visible in the bubble.
export function normalizeMarkdown(src: string): string {
  return src.replace(/\r\n/g, '\n').replace(/\uFF40/g, '`')
}
