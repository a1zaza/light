import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App, Login, RobotsPage } from './App';

const response = (status: number, body: unknown) =>
  ({ ok: status >= 200 && status < 300, status, json: async () => body }) as Response;
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
beforeEach(() => {
  location.hash = 'login';
});
describe('登录与公开访问', () => {
  it('登录失败显示错误并清除密码，保留用户名', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(401, { error: '账号或密码不正确' })));
    const onLogin = vi.fn();
    render(<Login onLogin={onLogin} />);
    fireEvent.change(screen.getByLabelText('管理员名称'), { target: { value: 'owner' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'incorrect-password' } });
    fireEvent.click(screen.getByRole('button', { name: '进入控制台' }));
    await screen.findByRole('alert');
    expect(screen.getByRole('alert').textContent).toContain('账号或密码不正确');
    expect((screen.getByLabelText('密码') as HTMLInputElement).value).toBe('');
    expect((screen.getByLabelText('管理员名称') as HTMLInputElement).value).toBe('owner');
    expect(onLogin).not.toHaveBeenCalled();
  });
  it('匿名页面只显示公开仓库入口，不出现管理导航', async () => {
    location.hash = 'public';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (path: string) =>
        path === '/api/session'
          ? response(401, {})
          : response(200, {
              items: [],
              page: 1,
              total: 0,
            }),
      ),
    );
    render(<App />);
    await screen.findByText('暂无公开仓库');
    expect(screen.queryByRole('button', { name: '机器人令牌' })).toBeNull();
    expect(screen.queryByRole('button', { name: '系统设置' })).toBeNull();
    expect(screen.getAllByRole('button', { name: '管理员登录' }).length).toBeGreaterThan(0);
  });
});
describe('机器人令牌', () => {
  it('创建时不出现权限范围，关闭一次性秘密后不再显示', async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn(async (_path: string, options?: RequestInit) =>
      options?.method === 'POST'
        ? response(201, {
            robot: { id: 'test', name: '测试 CI', username: 'robot-test' },
            secret: 'synthetic-one-time-secret',
          })
        : response(200, { items: [] }),
    );
    vi.stubGlobal('fetch', fetchMock);
    render(<RobotsPage revision={0} onRefresh={vi.fn()} />);
    await screen.findByText('还没有机器人令牌');
    await user.click(screen.getByRole('button', { name: '创建机器人令牌' }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getAllByRole('combobox').length).toBe(1);
    expect(within(dialog).queryByText('权限范围')).toBeNull();
    await user.type(within(dialog).getByLabelText(/机器人名称/), '测试 CI');
    await user.click(within(dialog).getByRole('button', { name: '创建令牌' }));
    await screen.findByText('synthetic-one-time-secret');
    await user.click(screen.getByRole('button', { name: '我已保存，关闭' }));
    await waitFor(() => expect(screen.queryByText('synthetic-one-time-secret')).toBeNull());
    const posted = JSON.parse(
      fetchMock.mock.calls.find(([, options]) => options?.method === 'POST')![1]!.body as string,
    );
    expect(Object.keys(posted).sort()).toEqual(['expiresAt', 'name']);
  });
});

describe('当前页面访问地址', () => {
  it('复制推送命令使用浏览器地址，而不是后端返回的旧地址', async () => {
    const previous = location.href;
    history.replaceState({}, '', '/console?view=images#repositories');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (path: string) =>
        path === '/api/session'
          ? response(200, { username: 'owner', csrf: 'synthetic-csrf' })
          : response(200, {
              items: [],
              page: 1,
              total: 0,
              externalURL: 'https://obsolete.example.com',
            }),
      ),
    );
    render(<App />);
    await screen.findByText(`docker push ${location.host}/personal/my-app:latest`);
    expect(
      screen.getByText(`docker tag my-app:latest ${location.host}/personal/my-app:latest`),
    ).toBeTruthy();
    expect(screen.queryByText(/obsolete\.example\.com/)).toBeNull();
    history.replaceState({}, '', previous);
  });
});

describe('仓库名称不可修改', () => {
  it('设置只允许修改说明和可见性，保存后仍使用原拉取地址', async () => {
    const user = userEvent.setup();
    const name = 'personal/app';
    location.hash = 'repositories/' + encodeURIComponent(name);
    const fetchMock = vi.fn(async (path: string, options?: RequestInit) => {
      if (path === '/api/session')
        return response(200, { username: 'owner', csrf: 'synthetic-csrf' });
      if (options?.method === 'PATCH')
        return response(200, { name, description: '更新说明', public: false, revision: 2 });
      if (path.startsWith('/api/admin/repository?'))
        return response(200, {
          repository: { name, description: '', public: false, revision: 1, updatedAt: '' },
          items: [],
          total: 0,
          page: 1,
        });
      return response(200, { status: 'ready' });
    });
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);
    const note = await screen.findByRole('textbox', { name: '备注' });
    await waitFor(() => expect((note as HTMLInputElement).disabled).toBe(false));
    expect(screen.queryByRole('button', { name: '仓库设置' })).toBeNull();
    expect(screen.queryByText('仓库名称不可修改')).toBeNull();
    expect(screen.queryByRole('textbox', { name: /仓库名称/ })).toBeNull();
    await user.type(note, '更新说明');
    expect(fetchMock.mock.calls.some(([, options]) => options?.method === 'PATCH')).toBe(false);
    await user.tab();
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([, options]) => options?.method === 'PATCH')).toBe(true),
    );
    const call = fetchMock.mock.calls.find(([, options]) => options?.method === 'PATCH')!;
    expect(call[0]).toBe('/api/admin/repository?name=personal%2Fapp');
    expect(JSON.parse(call[1]!.body as string)).toEqual({
      description: '更新说明',
      public: false,
      revision: 1,
    });
    expect(
      await screen.findByText(`docker pull ${location.host}/personal/app:latest`),
    ).toBeTruthy();
  });
});

it('机器人令牌弹窗的有效期列表留在弹窗内，选中后按选择提交', async () => {
  const user = userEvent.setup();
  let submitted: Record<string, unknown> | undefined;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_path: string, options?: RequestInit) => {
      if (options?.method === 'POST') {
        submitted = JSON.parse(options.body as string);
        return response(201, {
          robot: { id: 'test-expiry', name: '期限测试', username: 'robot-test' },
          secret: 'synthetic-fixture-secret',
        });
      }
      return response(200, { items: [] });
    }),
  );
  render(<RobotsPage revision={0} onRefresh={vi.fn()} />);
  await screen.findByText('还没有机器人令牌');
  await user.click(screen.getByRole('button', { name: '创建机器人令牌' }));
  const dialog = screen.getByRole('dialog', { name: '创建机器人令牌' });
  await user.type(within(dialog).getByLabelText('机器人名称'), '期限测试');
  const combo = within(dialog).getByRole('combobox', { name: '有效期' });
  await user.click(combo);
  await user.click(within(dialog).getByRole('option', { name: '永不过期' }));
  expect(combo.textContent).toContain('永不过期');
  expect(screen.queryByRole('listbox')).toBeNull();
  expect(submitted).toBeUndefined();
  await user.click(within(dialog).getByRole('button', { name: '创建令牌' }));
  await waitFor(() => expect(submitted).toEqual({ name: '期限测试', expiresAt: null }));
});
