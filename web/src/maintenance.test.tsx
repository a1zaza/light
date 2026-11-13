import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MaintenancePage, type MaintenanceJob, type MaintenanceState } from './maintenance';
const response = (body: unknown, status = 200) =>
  ({ ok: status >= 200 && status < 300, status, json: async () => body }) as Response;
const empty = (): MaintenanceState => ({
  active: null,
  jobs: [],
  error: '',
  catalogAt: '',
  catalogError: '',
  legacy: {},
});
const job = (kind: MaintenanceJob['kind'], mode: MaintenanceJob['mode']): MaintenanceJob => ({
  id: 'preview-id',
  kind,
  mode,
  status: 'completed',
  stage: 'finished',
  actor: 'admin',
  createdAt: new Date().toISOString(),
  finishedAt: new Date().toISOString(),
  expiresAt: new Date(Date.now() + 1800000).toISOString(),
  items: 3,
  files: 7,
  bytes: 4096,
  olderThanHours: 168,
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
describe('容器内存储维护', () => {
  it('先预览再确认执行，运行时禁止重复提交', async () => {
    const user = userEvent.setup();
    let state = empty();
    const requests: Record<string, unknown>[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_path: string, options?: RequestInit) => {
        if (options?.method === 'POST') {
          const body = JSON.parse(options.body as string);
          requests.push(body);
          if (body.action === 'gc-preview') {
            state = { ...state, jobs: [job('gc', 'preview')] };
            return response({ job: { ...job('gc', 'preview'), status: 'queued' } }, 202);
          }
          const active = {
            ...job('gc', 'execute'),
            id: 'execute-id',
            status: 'running',
            stage: 'cleaning',
          };
          state = { ...state, active, jobs: [active, { ...state.jobs[0], consumed: true }] };
          return response({ job: active }, 202);
        }
        return response(state);
      }),
    );
    render(<MaintenancePage revision={0} />);
    await screen.findByText('暂无维护任务');
    expect(
      (screen.getByRole('button', { name: '执行空间回收' }) as HTMLButtonElement).disabled,
    ).toBe(true);
    await user.click(screen.getByRole('button', { name: '预览空间回收' }));
    await waitFor(() =>
      expect(
        (screen.getByRole('button', { name: '执行空间回收' }) as HTMLButtonElement).disabled,
      ).toBe(false),
    );
    await user.click(screen.getByRole('button', { name: '执行空间回收' }));
    expect(requests).toEqual([{ action: 'gc-preview' }]);
    const modal = screen.getByRole('dialog', { name: '确认空间回收' });
    await user.click(within(modal).getByRole('button', { name: '确认执行' }));
    await screen.findByText('当前维护任务');
    expect(requests[1]).toEqual({ action: 'gc-execute', previewId: 'preview-id' });
    expect(
      (screen.getByRole('button', { name: '预览空间回收' }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(
      (screen.getByRole('button', { name: '重建仓库索引' }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(screen.queryByText(/docker compose|light gc/)).toBeNull();
  });
  it('上传清理按所选闲置时间发起，空预览不允许执行', async () => {
    const user = userEvent.setup();
    let state = empty();
    const requests: Record<string, unknown>[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_path: string, options?: RequestInit) => {
        if (options?.method === 'POST') {
          const body = JSON.parse(options.body as string);
          requests.push(body);
          const preview = {
            ...job('uploads', 'preview'),
            olderThanHours: 24,
            items: 0,
            files: 0,
            bytes: 0,
          };
          state = { ...state, jobs: [preview] };
          return response({ job: preview }, 202);
        }
        return response(state);
      }),
    );
    render(<MaintenancePage revision={0} />);
    await screen.findByText('暂无维护任务');
    await user.click(screen.getByRole('combobox', { name: '上传最短闲置时间' }));
    await user.click(screen.getByRole('option', { name: '1 天' }));
    await user.click(screen.getByRole('button', { name: '预览废弃上传' }));
    await screen.findByText('当前没有可清理的项目。');
    expect(requests[0]).toEqual({ action: 'uploads-preview', olderThanHours: 24 });
    expect(
      (screen.getByRole('button', { name: '执行上传清理' }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });
  it('重启后展示中断记录，不会自动重放删除任务', async () => {
    const state = {
      ...empty(),
      jobs: [
        {
          ...job('gc', 'execute'),
          status: 'interrupted',
          error: '上次维护因实例停止而中断，请重新预览',
        },
      ],
    };
    const fetchMock = vi.fn(async () => response(state));
    vi.stubGlobal('fetch', fetchMock);
    render(<MaintenancePage revision={0} />);
    await screen.findByText('已中断');
    expect(screen.getByText('上次维护因实例停止而中断，请重新预览')).toBeTruthy();
    expect(fetchMock.mock.calls.length).toBe(1);
  });
});
