import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { RepositoryHeader } from './repository-header';
import type { Repository } from './types';
const repository: Repository = {
  name: 'personal/app',
  description: '原备注',
  public: false,
  revision: 1,
  updatedAt: '',
};
const response = (value: unknown, status = 200) =>
  ({ ok: status >= 200 && status < 300, status, json: async () => value }) as Response;
function deferred() {
  let resolve!: (value: Response) => void;
  const promise = new Promise<Response>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
function show() {
  return render(
    <>
      <RepositoryHeader name={repository.name} repository={repository} publicView={false} />
      <button>下一项</button>
    </>,
  );
}
describe('仓库顶部直接编辑', () => {
  it('备注输入时不提交，失焦保存，未变化时不重复保存', async () => {
    const user = userEvent.setup();
    const fetchMock = vi
      .fn()
      .mockResolvedValue(response({ ...repository, description: '新备注', revision: 2 }));
    vi.stubGlobal('fetch', fetchMock);
    show();
    expect(screen.queryByText('已保存')).toBeNull();
    const note = screen.getByRole('textbox', { name: '备注' });
    await user.clear(note);
    await user.type(note, '新备注');
    expect(fetchMock).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: '下一项' }));
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      description: '新备注',
      public: false,
      revision: 1,
    });
    await user.click(note);
    await user.click(screen.getByRole('button', { name: '下一项' }));
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
  it('备注失焦后立即点击公开开关时串行合并，使用最新修订', async () => {
    const user = userEvent.setup(),
      first = deferred();
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockResolvedValueOnce(
        response({ ...repository, description: '新备注', public: true, revision: 3 }),
      );
    vi.stubGlobal('fetch', fetchMock);
    show();
    const note = screen.getByRole('textbox', { name: '备注' });
    await user.clear(note);
    await user.type(note, '新备注');
    await user.click(screen.getByRole('switch', { name: '公开仓库' }));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await act(async () =>
      first.resolve(response({ ...repository, description: '新备注', revision: 2 })),
    );
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({
      description: '新备注',
      public: true,
      revision: 2,
    });
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
    expect(screen.getByRole('switch').getAttribute('aria-checked')).toBe('true');
  });
  it('保存过程中继续输入不被旧响应覆盖，下一次失焦再保存', async () => {
    const user = userEvent.setup(),
      first = deferred();
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockResolvedValueOnce(response({ ...repository, description: '首次-继续', revision: 3 }));
    vi.stubGlobal('fetch', fetchMock);
    show();
    const note = screen.getByRole('textbox', { name: '备注' }) as HTMLInputElement;
    await user.clear(note);
    await user.type(note, '首次');
    await user.click(screen.getByRole('button', { name: '下一项' }));
    await user.click(note);
    await user.type(note, '-继续');
    await act(async () =>
      first.resolve(response({ ...repository, description: '首次', revision: 2 })),
    );
    expect(note.value).toBe('首次-继续');
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole('button', { name: '下一项' }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(JSON.parse(fetchMock.mock.calls[1][1].body).description).toBe('首次-继续');
  });
  it('保存失败保留备注，重试成功；公开开关失败会回退', async () => {
    const user = userEvent.setup();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response({ error: '写入失败' }, 500))
      .mockResolvedValueOnce(response({ ...repository, description: '待保存', revision: 2 }))
      .mockResolvedValueOnce(response({ error: '稍后重试' }, 503));
    vi.stubGlobal('fetch', fetchMock);
    show();
    const note = screen.getByRole('textbox', { name: '备注' }) as HTMLInputElement;
    await user.clear(note);
    await user.type(note, '待保存');
    await user.click(screen.getByRole('button', { name: '下一项' }));
    await screen.findByRole('alert');
    expect(note.value).toBe('待保存');
    await user.click(screen.getByRole('button', { name: '重试保存' }));
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
    await user.click(screen.getByRole('switch'));
    await screen.findByRole('alert');
    expect(screen.getByRole('switch').getAttribute('aria-checked')).toBe('false');
    expect(note.value).toBe('待保存');
  });
  it('标签列表刷新暂时缺少元数据或返回旧修订时不覆盖当前编辑', async () => {
    const user = userEvent.setup();
    const fetchMock = vi
      .fn()
      .mockResolvedValue(response({ ...repository, public: true, revision: 2 }));
    vi.stubGlobal('fetch', fetchMock);
    const view = render(
      <RepositoryHeader name={repository.name} repository={repository} publicView={false} />,
    );
    await user.click(screen.getByRole('switch'));
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
    const note = screen.getByRole('textbox', { name: '备注' }) as HTMLInputElement;
    await user.type(note, '继续编辑');
    view.rerender(<RepositoryHeader name={repository.name} publicView={false} />);
    expect(note.value).toBe('原备注继续编辑');
    view.rerender(
      <RepositoryHeader name={repository.name} repository={repository} publicView={false} />,
    );
    expect(note.value).toBe('原备注继续编辑');
    expect(screen.getByRole('switch').getAttribute('aria-checked')).toBe('true');
  });
  it('公开浏览只展示备注，不提供编辑和公开切换', () => {
    vi.stubGlobal('fetch', vi.fn());
    render(
      <RepositoryHeader
        name={repository.name}
        repository={{ ...repository, public: true }}
        publicView
      />,
    );
    expect(screen.getByText('原备注')).toBeTruthy();
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(screen.queryByRole('switch')).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
  });
});

it('外部修订变化时先报告冲突，保留备注供用户核对后重试', async () => {
  const user = userEvent.setup();
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response({ error: '数据已更新，请核对后重试' }, 409))
    .mockResolvedValueOnce(response({ ...repository, description: '本地备注', revision: 3 }));
  vi.stubGlobal('fetch', fetchMock);
  const view = render(
    <>
      <RepositoryHeader name={repository.name} repository={repository} publicView={false} />
      <button>下一项</button>
    </>,
  );
  const note = screen.getByRole('textbox', { name: '备注' }) as HTMLInputElement;
  await user.clear(note);
  await user.type(note, '本地备注');
  view.rerender(
    <>
      <RepositoryHeader
        name={repository.name}
        repository={{ ...repository, description: '其他端修改', revision: 2 }}
        publicView={false}
      />
      <button>下一项</button>
    </>,
  );
  await user.click(screen.getByRole('button', { name: '下一项' }));
  await screen.findByRole('alert');
  expect(note.value).toBe('本地备注');
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(JSON.parse(fetchMock.mock.calls[0][1].body).revision).toBe(1);
  await user.click(screen.getByRole('button', { name: '重试保存' }));
  await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
  expect(JSON.parse(fetchMock.mock.calls[1][1].body).revision).toBe(2);
});
