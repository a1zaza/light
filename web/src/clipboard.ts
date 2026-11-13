// Clipboard API is unavailable on ordinary HTTP origins. Keep the fallback in
// the click handler's activation, and inside the active dialog when present.
export async function copyText(value: string): Promise<void> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value);
      return;
    }
  } catch {
    // Permission errors and browser restrictions can still allow legacy copy.
  }

  const active = document.activeElement;
  const control =
    active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement ? active : null;
  const inputSelection = control
    ? {
        start: control.selectionStart,
        end: control.selectionEnd,
        direction: control.selectionDirection,
      }
    : null;
  const selection = window.getSelection();
  const ranges = selection
    ? Array.from({ length: selection.rangeCount }, (_, index) =>
        selection.getRangeAt(index).cloneRange(),
      )
    : [];
  const helper = document.createElement('textarea');
  helper.value = value;
  helper.readOnly = true;
  helper.tabIndex = -1;
  helper.setAttribute('aria-hidden', 'true');
  helper.className = 'clipboard-helper';
  (active?.closest('[role="dialog"]') || document.body).appendChild(helper);
  try {
    helper.focus({ preventScroll: true });
    helper.select();
    helper.setSelectionRange(0, value.length);
    if (typeof document.execCommand !== 'function' || !document.execCommand('copy')) {
      throw new Error('无法自动复制');
    }
  } finally {
    helper.remove();
    if (active instanceof HTMLElement) active.focus({ preventScroll: true });
    if (control && inputSelection?.start != null && inputSelection.end != null) {
      control.setSelectionRange(
        inputSelection.start,
        inputSelection.end,
        inputSelection.direction || undefined,
      );
    }
    if (selection) {
      selection.removeAllRanges();
      for (const range of ranges) selection.addRange(range);
    }
  }
}
