import { useEffect, useId, useRef, useState } from 'react';
import { Globe2, LoaderCircle } from 'lucide-react';
import { api, query } from './api';
import { Badge, ErrorBox } from './components';
import type { Repository } from './types';

type Settings = { description: string; public: boolean };
export function RepositoryHeader({
  name,
  repository,
  publicView,
}: {
  name: string;
  repository?: Repository;
  publicView: boolean;
}) {
  const noteId = useId(),
    statusId = useId();
  const [saved, setSaved] = useState<Repository>(),
    [note, setNote] = useState(''),
    [visible, setVisible] = useState(false),
    [saving, setSaving] = useState(false),
    [error, setError] = useState(''),
    [settled, setSettled] = useState(0);
  const server = useRef<Repository | undefined>(undefined),
    desired = useRef<Settings>({ description: '', public: false }),
    draft = useRef(''),
    focused = useRef(false),
    pending = useRef(false),
    mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  // Search, pagination and refresh may temporarily omit metadata or return an
  // older snapshot. Keep local edits and never roll back an acknowledged save.
  useEffect(() => {
    if (!repository || repository.name !== name || pending.current) return;
    const previous = server.current;
    if (
      previous &&
      (repository.revision <= previous.revision ||
        (focused.current && draft.current !== previous.description))
    )
      return;
    server.current = repository;
    setSaved(repository);
    if (!previous || desired.current.description === previous.description)
      desired.current.description = repository.description;
    if (!previous || desired.current.public === previous.public) {
      desired.current.public = repository.public;
      setVisible(repository.public);
    }
    if (!previous || draft.current === previous.description) {
      draft.current = repository.description;
      setNote(repository.description);
    }
  }, [repository, name, settled]);
  async function flush() {
    if (publicView || pending.current || !server.current) return;
    if (
      desired.current.description === server.current.description &&
      desired.current.public === server.current.public
    )
      return;
    pending.current = true;
    if (mounted.current) {
      setSaving(true);
      setError('');
    }
    try {
      while (
        server.current &&
        (desired.current.description !== server.current.description ||
          desired.current.public !== server.current.public)
      ) {
        const update: Settings & { revision: number } = {
          ...desired.current,
          revision: server.current.revision,
        };
        const result: Repository = await api<Repository>(
          `/admin/repository?${query({ name })}`,
          'PATCH',
          update,
        );
        server.current = result;
        if (mounted.current) setSaved(result);
      }
    } catch (e) {
      // Keep unsaved text for retry, but revert a failed visibility change.
      if (server.current) {
        desired.current = {
          description: server.current.description,
          public: server.current.public,
        };
        if (mounted.current) setVisible(server.current.public);
      }
      if (mounted.current) setError((e as Error).message);
    } finally {
      pending.current = false;
      if (mounted.current) {
        setSaving(false);
        setSettled((value) => value + 1);
      }
    }
  }
  function saveNote() {
    focused.current = false;
    desired.current.description = draft.current;
    void flush();
  }
  function toggle() {
    desired.current.public = !desired.current.public;
    setVisible(desired.current.public);
    void flush();
  }
  const dirty = !!saved && note !== saved.description;
  const status = saving
    ? '正在保存…'
    : error
      ? '保存失败'
      : dirty
        ? '失去焦点后自动保存'
        : saved
          ? ''
          : '正在读取…';
  return (
    <section className="repository-header" aria-label="仓库信息">
      <div className="repository-title-row">
        <div className="repository-identity">
          <span className="eyebrow">镜像仓库 / 详情</span>
          <h1>{name}</h1>
        </div>
        {publicView && saved && (
          <Badge color="blue">
            <Globe2 size={12} />
            公开仓库
          </Badge>
        )}
      </div>
      {publicView ? (
        <p className="repository-public-note">{saved?.description || '暂无备注'}</p>
      ) : (
        <>
          <div className="repository-note-row">
            <input
              id={noteId}
              aria-label="备注"
              value={note}
              placeholder="添加备注"
              maxLength={600}
              disabled={!saved}
              aria-describedby={status ? statusId : undefined}
              onFocus={() => {
                focused.current = true;
              }}
              onChange={(e) => {
                draft.current = e.target.value;
                setNote(e.target.value);
              }}
              onBlur={saveNote}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  e.currentTarget.blur();
                }
              }}
            />
            <div className="repository-visibility-control">
              <span className={!visible ? 'active' : ''}>私有</span>
              <button
                type="button"
                role="switch"
                aria-label="公开仓库"
                aria-checked={visible}
                disabled={!saved}
                className="visibility-switch"
                onClick={toggle}
              >
                <span />
              </button>
              <span className={visible ? 'active' : ''}>公开</span>
            </div>
          </div>
          {status && (
            <div className="repository-note-feedback">
              <span
                id={statusId}
                className={`repository-save-state ${error ? 'failed' : ''}`}
                role="status"
                aria-live="polite"
              >
                {saving && <LoaderCircle size={13} className="spin" />}
                {status}
              </span>
              {error && dirty && (
                <button
                  type="button"
                  className="secondary small-button"
                  disabled={saving}
                  onClick={saveNote}
                >
                  重试保存
                </button>
              )}
            </div>
          )}
          <ErrorBox error={error ? `保存失败：${error}` : ''} />
        </>
      )}
    </section>
  );
}
