import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {
  ApplicationSettingsPanel,
  type ApplicationSettings,
  type SettingsResponse,
} from './application-settings';
const settings: ApplicationSettings = {
  revision: 1,
  rateLimitIPHeader: '',
  hubEnabled: false,
  hubCacheMaxBytes: 10 * 1024 ** 3,
  hubMaxBlobBytes: 20 * 1024 ** 3,
  hubFetchConcurrency: 4,
  hubFetchTimeoutSeconds: 1800,
};
const response = (value: unknown, status = 200) =>
  ({ ok: status >= 200 && status < 300, status, json: async () => value }) as Response;
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
it('读取默认设置、提交版本和单位换算，保存即时生效', async () => {
  const user = userEvent.setup();
  let sent: ApplicationSettings | undefined;
  const fetcher = vi.fn(async (_path: string, options?: RequestInit) => {
    if (options?.method === 'PATCH') {
      sent = JSON.parse(options.body as string);
      return response({
        settings: { ...sent, revision: 2 },
        effective: { ...sent, revision: 2 },
      });
    }
    return response({
      settings,
      effective: settings,
    } satisfies SettingsResponse);
  });
  vi.stubGlobal('fetch', fetcher);
  render(<ApplicationSettingsPanel />);
  const header = await screen.findByLabelText('限流客户端 IP Header');
  await user.type(header, 'X-Light-IP');
  await user.click(screen.getByRole('switch', { name: '启用 Docker Hub 代理' }));
  const capacity = screen.getByLabelText('缓存容量（GiB）');
  await user.clear(capacity);
  await user.type(capacity, '12.5');
  await user.click(screen.getByRole('button', { name: '保存设置' }));
  await screen.findByText('配置已生效。');
  expect(sent).toEqual({
    ...settings,
    rateLimitIPHeader: 'X-Light-IP',
    hubEnabled: true,
    hubCacheMaxBytes: 12.5 * 1024 ** 3,
  });
  expect(screen.getByText('即时生效')).toBeTruthy();
  expect((screen.getByRole('button', { name: '保存设置' }) as HTMLButtonElement).disabled).toBe(
    true,
  );
});
it('版本冲突保留编辑内容，显式重新读取后获取新版本', async () => {
  const user = userEvent.setup();
  let reads = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_path: string, options?: RequestInit) => {
      if (options?.method === 'PATCH') return response({ error: '数据已更新，请刷新后重试' }, 409);
      reads++;
      const saved = {
        ...settings,
        revision: reads,
        rateLimitIPHeader: reads > 1 ? 'X-Other-IP' : '',
      };
      return response({ settings: saved, effective: settings });
    }),
  );
  render(<ApplicationSettingsPanel />);
  const header = await screen.findByLabelText('限流客户端 IP Header');
  await user.type(header, 'X-Draft');
  await user.click(screen.getByRole('button', { name: '保存设置' }));
  await screen.findByText('数据已更新，请刷新后重试');
  expect((header as HTMLInputElement).value).toBe('X-Draft');
  await user.click(screen.getByRole('button', { name: '丢弃修改并重新读取' }));
  await waitFor(() =>
    expect((screen.getByLabelText('限流客户端 IP Header') as HTMLInputElement).value).toBe(
      'X-Other-IP',
    ),
  );
});
