import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HubCachePanel, type HubCacheState } from './hub-cache';
const state: HubCacheState = {
  enabled: true,
  bytes: 1,
  maxBytes: 100,
  reserved: 0,
  blobs: 1,
  downloading: 0,
  cleaning: false,
  error: '',
  lastClean: '',
  entries: [
    {
      name: 'library/alpine',
      ref: 'latest',
      digest: 'sha256:test',
      mediaType: 'index',
      checkedAt: '',
      bytes: 1,
      complete: false,
      expiresAt: '',
    },
  ],
};
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
it('显示代理私有来源，清空需确认并调用独立接口', async () => {
  const user = userEvent.setup(),
    refresh = vi.fn();
  const fetcher = vi.fn(async () => ({
    ok: true,
    status: 202,
    json: async () => ({ ...state, cleaning: true }),
  }));
  vi.stubGlobal('fetch', fetcher);
  render(<HubCachePanel data={state} refresh={refresh} />);
  expect(screen.getByText('私有')).toBeTruthy();
  expect(screen.getByText('未完整缓存')).toBeTruthy();
  await user.click(screen.getByRole('button', { name: '清空代理缓存' }));
  expect(fetcher).not.toHaveBeenCalled();
  const modal = screen.getByRole('dialog', { name: '清空 Docker Hub 代理缓存' });
  await user.click(within(modal).getByRole('button', { name: '确认清空' }));
  expect(fetcher).toHaveBeenCalledWith(
    '/api/admin/hub-cache',
    expect.objectContaining({ method: 'POST', body: JSON.stringify({ action: 'all' }) }),
  );
  expect(refresh).toHaveBeenCalledTimes(1);
});
it('清理进行中时禁止重复提交', () => {
  render(<HubCachePanel data={{ ...state, cleaning: true }} refresh={() => {}} />);
  expect((screen.getByRole('button', { name: '清理过期缓存' }) as HTMLButtonElement).disabled).toBe(
    true,
  );
  expect((screen.getByRole('button', { name: '清空代理缓存' }) as HTMLButtonElement).disabled).toBe(
    true,
  );
});
