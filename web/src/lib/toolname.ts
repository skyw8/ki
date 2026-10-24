export function canonicalToolName(name: string): string {
  const snake = name.replace(/([A-Z]+)([A-Z][a-z])/g, '$1_$2').replace(/([a-z0-9])([A-Z])/g, '$1_$2').toLowerCase()
  return TOOL_ALIASES[snake] ?? snake
}

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
