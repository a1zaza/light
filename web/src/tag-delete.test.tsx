import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { RepositoryPage } from './App';

const response = (value: unknown) =>
  ({ ok: true, status: 200, json: async () => value }) as Response;
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
it('同摘要多标签按所选标签预览、确认，并显示清理失败反馈', async () => {
  const user = userEvent.setup();
  const onRefresh = vi.fn();
  const fetchMock = vi.fn(async (path: string, options?: RequestInit) => {
    if (path === '/api/admin/delete-preview')
      return response({
        id: 'preview',
        preview: {
          name: 'demo/app',
          tag: 'stable',
          digest: 'sha256:shared',
          bytes: 0,
          blobs: 0,
          fingerprint: 'snapshot',
        },
      });
    if (path === '/api/admin/delete')
      return response({ ok: true, warning: '标签已删除，部分存储清理失败' });
    return response({
      repository: { name: 'demo/app', description: '', public: false, revision: 1 },
      items: ['latest', 'stable'].map((tag) => ({
        tag,
        digest: 'sha256:shared',
        size: 12,
        platforms: ['linux/amd64'],
      })),
      page: 1,
      total: 2,
    });
  });
  vi.stubGlobal('fetch', fetchMock);
  render(<RepositoryPage name="demo/app" publicView={false} revision={0} onRefresh={onRefresh} />);
  await user.click(await screen.findByRole('button', { name: '删除 stable' }));
  const dialog = await screen.findByRole('dialog');
  expect(within(dialog).queryByText(/同时移除/)).toBeNull();
  expect(within(dialog).getByText(/仅删除标签 stable/)).toBeTruthy();
  const previewCall = fetchMock.mock.calls.find(([path]) => path === '/api/admin/delete-preview')!;
  expect(JSON.parse(previewCall[1]!.body as string)).toEqual({ name: 'demo/app', tag: 'stable' });
  await user.click(within(dialog).getByRole('button', { name: '确认删除标签' }));
  await waitFor(() => expect(onRefresh).toHaveBeenCalledTimes(1));
  expect(await screen.findByText('标签已删除，部分存储清理失败')).toBeTruthy();
  expect(screen.queryByRole('dialog')).toBeNull();
});
