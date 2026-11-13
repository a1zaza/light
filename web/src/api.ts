export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}
let csrf = '';
export function setCSRF(value: string) {
  csrf = value;
}
export async function api<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(`/api${path}`, {
    method,
    credentials: 'same-origin',
    headers: {
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(csrf && method !== 'GET' ? { 'X-CSRF-Token': csrf } : {}),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const value = await response.json().catch(() => ({}));
  if (!response.ok) {
    if (response.status === 401 && path.startsWith('/admin/'))
      window.dispatchEvent(new Event('light-session-expired'));
    throw new APIError(response.status, value.error || `请求失败（${response.status}）`);
  }
  return value as T;
}
export const query = (values: Record<string, string | number>) =>
  new URLSearchParams(
    Object.fromEntries(Object.entries(values).map(([k, v]) => [k, String(v)])),
  ).toString();
export function size(value: number) {
  if (!value) return '0 B';
  const unit = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const n = Math.min(Math.floor(Math.log(value) / Math.log(1024)), 4);
  return `${(value / 1024 ** n).toFixed(n === 0 ? 0 : 1)} ${unit[n]}`;
}
export const time = (value?: string) =>
  value && !value.startsWith('0001-')
    ? new Date(value).toLocaleString('zh-CN', { hour12: false })
    : '—';
export const registryHost = () => window.location.host;
