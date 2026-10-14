// Older remote agents may still return terminal color codes in their log entries.
export function plainLogText(value: string): string {
  return value.replace(/[\u001b\u241b]\[[0-?]*[ -/]*[@-~]/g, '')
}
