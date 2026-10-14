// Older remote agents may still return terminal color codes in their log entries.
export function plainLogText(value: string): string {
  return value.replace(/[\u001b\u241b]\[[0-?]*[ -/]*[@-~]/g, '')
}

export function formatLogMessage(value: string): string {
  const text = plainLogText(value)
  const prefix = /^(?:TRACE|DEBUG|INFO|WARN|ERROR|FATAL|PANIC|UNKNOWN)(?:\[-?\d+\])?[ \t]+/.exec(text)
  if (!prefix) return text
  return text.slice(prefix[0].length)
    .replace(/^\[\d+ [^\]\r\n]+\][ \t]+/, '')
    .replace(/^[^\s:]+:[ \t]+/, '')
}

const logTimeFormatter = new Intl.DateTimeFormat('zh-CN', {
  hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3, hourCycle: 'h23',
})

export function formatLogTime(value: string): string {
  return logTimeFormatter.format(new Date(value))
}
