export function loopbackAPITarget(value: string | undefined): string {
  const target = value || 'http://127.0.0.1:8080'
  const url = new URL(target)
  if (
    url.protocol !== 'http:' || url.hostname !== '127.0.0.1' ||
    url.username || url.password || url.pathname !== '/' || url.search || url.hash
  ) {
    throw new Error('AGENT_PLATFORM_DEV_API_URL must be an HTTP 127.0.0.1 origin')
  }
  return url.origin
}
