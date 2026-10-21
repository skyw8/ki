/** Match the Go tool-name adapter; schemas and UI display use one canonical spelling. */
export function canonicalToolName(name: string): string {
  const snake = name.replace(/([A-Z]+)([A-Z][a-z])/g, '$1_$2').replace(/([a-z0-9])([A-Z])/g, '$1_$2').toLowerCase()
  return TOOL_ALIASES[snake] ?? snake
}

/**
 * Provider-native spellings for the same capability. Transcripts keep the raw
 * name; only rendering collapses them, so a Claude Code `Bash` call and a Ki
 * `exec_command` call share one card instead of falling through to the generic
 * JSON view.
 */
const TOOL_ALIASES: Record<string, string> = {
  bash: 'exec_command',
  run_command: 'exec_command',
  shell_command: 'exec_command',
  task_output: 'write_stdin',
  agent: 'spawn_agent',
  task: 'spawn_agent',
  web_fetch: 'fetch_content',
  web_search: 'deep_web_search',
  str_replace_editor: 'edit',
}
