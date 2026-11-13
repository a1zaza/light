import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { RepositoryPage } from './App';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
it('多架构镜像在列表和详情显示创建时间', async () => {
  const created = '2026-11-12T03:00:00Z';
  const image = {
    tag: 'latest',
    digest: 'sha256:multi',
    mediaType: 'application/vnd.oci.image.index.v1+json',
    size: 100,
    platforms: ['linux/amd64', 'linux/arm64'],
    created,
    raw: { schemaVersion: 2 },
  };
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string) => ({
      ok: true,
      status: 200,
      json: async () =>
        path.startsWith('/api/admin/image?')
          ? image
          : {
              repository: { name: 'demo/multi', description: '', public: false, revision: 1 },
              items: [image],
              page: 1,
              total: 1,
            },
    })),
  );
  render(<RepositoryPage name="demo/multi" publicView={false} revision={0} onRefresh={vi.fn()} />);
  const formatted = new Date(created).toLocaleString('zh-CN', { hour12: false });
  expect(await screen.findByText(formatted)).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: '查看 latest' }));
  const dialog = await screen.findByRole('dialog');
  expect(within(dialog).getByText('镜像创建时间')).toBeTruthy();
  expect(within(dialog).getByText(formatted)).toBeTruthy();
});
