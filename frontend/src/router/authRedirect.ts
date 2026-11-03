/** Same-origin router destinations only, matching the existing OAuth callback policy. */
export function safeAuthRedirect(value: unknown, fallback = '/dashboard'): string {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') ||
      value.includes('://') || /[\\\r\n\u0000-\u001f]/.test(value)) return fallback
  return value
}

export function authDestination(path: '/login' | '/register', redirect: unknown) {
  return { path, query: { redirect: safeAuthRedirect(redirect) } }
}
