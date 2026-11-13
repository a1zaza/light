import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { copyText } from './clipboard';
import { CopyText, Modal } from './components';

const originalExec = Object.getOwnPropertyDescriptor(document, 'execCommand');
function legacyCopy(implementation: () => boolean) {
  const copy = vi.fn(implementation);
  Object.defineProperty(document, 'execCommand', { configurable: true, value: copy });
  return copy;
}
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  if (originalExec) Object.defineProperty(document, 'execCommand', originalExec);
  else Reflect.deleteProperty(document, 'execCommand');
});
it('优先使用 Clipboard API，复制完整内容', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal('navigator', { clipboard: { writeText } });
  const fallback = legacyCopy(() => true);
  const value = 'docker pull registry.test/team/app@sha256:' + 'a'.repeat(64);
  await copyText(value);
  expect(writeText).toHaveBeenCalledWith(value);
  expect(fallback).not.toHaveBeenCalled();
  expect(document.querySelector('.clipboard-helper')).toBeNull();
});
it('普通 HTTP 缺少 Clipboard API 时，弹窗内复制完整文本并恢复焦点', async () => {
  vi.stubGlobal('navigator', {});
  const value = '完整秘密与命令\n' + 'long-value'.repeat(30);
  const fallback = legacyCopy(() => {
    const helper = document.activeElement as HTMLTextAreaElement;
    expect(helper.tagName).toBe('TEXTAREA');
    expect(helper.value).toBe(value);
    expect(helper.selectionStart).toBe(0);
    expect(helper.selectionEnd).toBe(value.length);
    expect(helper.closest('[role="dialog"]')).not.toBeNull();
    return true;
  });
  render(
    <Modal title="复制验证" onClose={vi.fn()}>
      <CopyText block value={value} label="省略显示" />
    </Modal>,
  );
  const button = screen.getByRole('button', { name: '复制' });
  button.focus();
  fireEvent.click(button);
  await screen.findByRole('button', { name: '已复制' });
  expect(fallback).toHaveBeenCalledWith('copy');
  expect(document.activeElement).toBe(button);
  expect(document.querySelector('.clipboard-helper')).toBeNull();
});
it('Clipboard API 拒绝访问时回退，不显示错误', async () => {
  vi.stubGlobal('navigator', {
    clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
  });
  const fallback = legacyCopy(() => true);
  render(<CopyText value="完整摘要" />);
  fireEvent.click(screen.getByRole('button', { name: '复制' }));
  await screen.findByRole('button', { name: '已复制' });
  expect(fallback).toHaveBeenCalledOnce();
  expect(screen.queryByRole('status')).toBeNull();
});
it.each(['false', 'throw', 'missing'])(
  '回退失败（%s）时清理临时节点并恢复输入选区',
  async (mode) => {
    vi.stubGlobal('navigator', {});
    if (mode === 'missing') Reflect.deleteProperty(document, 'execCommand');
    else
      legacyCopy(() => {
        if (mode === 'throw') throw new Error('blocked');
        return false;
      });
    const input = document.createElement('input');
    document.body.appendChild(input);
    input.value = 'before copy';
    input.focus();
    input.setSelectionRange(2, 5, 'backward');
    try {
      await expect(copyText('full contents')).rejects.toThrow();
      expect(document.querySelector('.clipboard-helper')).toBeNull();
      expect(document.activeElement).toBe(input);
      expect([input.selectionStart, input.selectionEnd, input.selectionDirection]).toEqual([
        2,
        5,
        'backward',
      ]);
    } finally {
      input.remove();
    }
  },
);
it('复制失败显示手动复制提示，重试成功清除错误', async () => {
  vi.stubGlobal('navigator', {});
  legacyCopy(() => false);
  render(<CopyText block value="可手动选中的完整内容" />);
  fireEvent.click(screen.getByRole('button', { name: '复制' }));
  await screen.findByText('无法自动复制，请选中文本后复制。');
  expect(screen.queryByRole('button', { name: '已复制' })).toBeNull();
  legacyCopy(() => true);
  fireEvent.click(screen.getByRole('button', { name: '复制' }));
  await screen.findByRole('button', { name: '已复制' });
  await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
});
