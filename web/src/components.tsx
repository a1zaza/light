import {
  cloneElement,
  isValidElement,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from 'react';
import { Select } from './select';
import { copyText } from './clipboard';
export { Select, SelectOption } from './select';
import { Check, ChevronLeft, ChevronRight, Copy, LoaderCircle, Package, X } from 'lucide-react';

export function ErrorBox({ error }: { error: string }) {
  return error ? (
    <div className="alert error" role="alert">
      {error}
    </div>
  ) : null;
}
export function Loading() {
  return (
    <div className="loading" role="status">
      <LoaderCircle className="spin" size={20} />
      正在读取数据…
    </div>
  );
}
export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <Package size={32} />
      <h2>{title}</h2>
      {children && <div>{children}</div>}
    </div>
  );
}
export function Badge({ children, color = '' }: { children: ReactNode; color?: string }) {
  return <span className={`badge ${color}`}>{children}</span>;
}
export function Heading({
  eyebrow,
  title,
  description,
  children,
}: {
  eyebrow?: string;
  title: string;
  description: string;
  children?: ReactNode;
}) {
  return (
    <div className="page-heading">
      <div>
        {eyebrow && <span className="eyebrow">{eyebrow}</span>}
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      <div className="heading-actions">{children}</div>
    </div>
  );
}
export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactNode;
  hint?: string;
}) {
  const generatedId = useId(),
    hintId = `${generatedId}-hint`;
  const child = isValidElement(children)
    ? (children as ReactElement<Record<string, unknown>>)
    : null;
  const control =
    !!child &&
    (child.type === Select || ['input', 'textarea', 'select'].includes(String(child.type)));
  const inputId = typeof child?.props.id === 'string' ? child.props.id : generatedId;
  return (
    <div className="field">
      {control ? <label htmlFor={inputId}>{label}</label> : <span>{label}</span>}
      {control && child
        ? cloneElement(child, {
            id: inputId,
            'aria-describedby':
              [child.props['aria-describedby'], hint ? hintId : ''].filter(Boolean).join(' ') ||
              undefined,
          })
        : children}
      {hint && <small id={hintId}>{hint}</small>}
    </div>
  );
}
export function CopyText({
  value,
  label,
  block = false,
}: {
  value: string;
  label?: string;
  block?: boolean;
}) {
  const [copied, setCopied] = useState(false),
    [error, setError] = useState('');
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );
  async function copy() {
    try {
      await copyText(value);
      setCopied(true);
      setError('');
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 2000);
    } catch {
      if (timer.current) clearTimeout(timer.current);
      setCopied(false);
      setError('无法自动复制，请选中文本后复制。');
    }
  }
  return (
    <div className={block ? 'copy-block' : 'copy-inline'}>
      <code title={value}>{label || value}</code>
      <button
        className="icon-button"
        aria-label={copied ? '已复制' : '复制'}
        title={copied ? '已复制' : '复制'}
        onClick={() => void copy()}
      >
        {copied ? <Check size={14} /> : <Copy size={14} />}
      </button>
      {error && <small role="status">{error}</small>}
    </div>
  );
}
export function Pagination({
  page,
  total,
  onChange,
}: {
  page: number;
  total: number;
  onChange: (page: number) => void;
}) {
  return (
    <div className="pagination">
      <span>
        共 {total} 条 · 第 {page} 页
      </span>
      <div>
        <button
          className="secondary small-button"
          disabled={page <= 1}
          aria-label="上一页"
          onClick={() => onChange(page - 1)}
        >
          <ChevronLeft size={14} />
        </button>
        <button
          className="secondary small-button"
          disabled={page * 20 >= total}
          aria-label="下一页"
          onClick={() => onChange(page + 1)}
        >
          <ChevronRight size={14} />
        </button>
      </div>
    </div>
  );
}
export function Modal({
  title,
  children,
  onClose,
  busy = false,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  busy?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const node = ref.current;
    node?.focus();
    return () => previous?.focus();
  }, []);
  return (
    <div
      className="modal-mask"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && !busy) close.current();
      }}
    >
      <div
        ref={ref}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && !busy) close.current();
          if (e.key === 'Tab') {
            const items = Array.from(
              ref.current?.querySelectorAll<HTMLElement>(
                'button:not(:disabled),input:not(:disabled),textarea:not(:disabled),select:not(:disabled),a[href]',
              ) || [],
            ).filter((node) => node.tabIndex >= 0 && node.getAttribute('aria-hidden') !== 'true');
            const first = items[0],
              last = items.at(-1);
            if (!first) {
              e.preventDefault();
              return;
            }
            if (
              e.shiftKey &&
              (document.activeElement === first || document.activeElement === ref.current)
            ) {
              e.preventDefault();
              last?.focus();
            } else if (
              !e.shiftKey &&
              (document.activeElement === last || document.activeElement === ref.current)
            ) {
              e.preventDefault();
              first.focus();
            }
          }
        }}
      >
        <header className="modal-header">
          <div>
            <span className="eyebrow">light / 镜像仓库</span>
            <h2>{title}</h2>
          </div>
          <button
            className="icon-button"
            disabled={busy}
            aria-label="关闭弹窗"
            onClick={() => close.current()}
          >
            <X size={18} />
          </button>
        </header>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  );
}
