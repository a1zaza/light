import { useState } from 'react';
import { api, size, time } from './api';
import { Badge, CopyText, ErrorBox, Modal } from './components';
export type HubCacheState = {
  enabled: boolean;
  bytes: number;
  maxBytes: number;
  reserved: number;
  blobs: number;
  downloading: number;
  cleaning: boolean;
  error: string;
  lastClean: string;
  entries: {
    name: string;
    ref: string;
    digest: string;
    mediaType: string;
    checkedAt: string;
    bytes: number;
    complete: boolean;
    expiresAt: string;
  }[];
};
export function HubCachePanel({ data, refresh }: { data: HubCacheState; refresh: () => void }) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(''),
    [confirm, setConfirm] = useState(false);
  async function clean(action: string) {
    setBusy(true);
    setError('');
    try {
      await api('/admin/hub-cache', 'POST', { action });
      setConfirm(false);
      refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const disabled = busy || data.cleaning || !!data.error;
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2>Docker Hub 代理缓存</h2>
        <Badge>{data.enabled ? '私有' : '已关闭'}</Badge>
      </div>
      <div className="panel-body">
        <p>
          已缓存 {size(data.bytes)} / {size(data.maxBytes)} · {data.blobs} 个 blob ·{' '}
          {data.downloading} 个回源任务 · 预留 {size(data.reserved)}
        </p>
        <p className="muted">
          清单每次拉取时检查 Docker Hub；blob 完整写入后保留 7 天，读取不续期。最近清理：
          {time(data.lastClean)}
        </p>
        {data.cleaning && <p role="status">正在等待在途读取并清理代理缓存…</p>}
        <ErrorBox error={error || data.error} />
        <div className="maintenance-actions">
          <button className="secondary" disabled={disabled} onClick={() => void clean('expired')}>
            清理过期缓存
          </button>
          <button className="danger-button" disabled={disabled} onClick={() => setConfirm(true)}>
            清空代理缓存
          </button>
        </div>
      </div>
      {data.entries.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>来源 / 上游仓库</th>
                <th>标签 / 摘要</th>
                <th>最后回源</th>
                <th>已缓存 blob</th>
                <th>最早到期</th>
              </tr>
            </thead>
            <tbody>
              {data.entries.map((entry) => (
                <tr key={`${entry.name}@${entry.ref}`}>
                  <td>
                    <Badge>Docker Hub 代理</Badge>
                    <p>{entry.name}</p>
                  </td>
                  <td>
                    {entry.ref}
                    <CopyText value={entry.digest} />
                  </td>
                  <td>{time(entry.checkedAt)}</td>
                  <td>
                    {size(entry.bytes)}
                    <p className="muted">{entry.complete ? '当前清单层已缓存' : '未完整缓存'}</p>
                  </td>
                  <td>{time(entry.expiresAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {confirm && (
        <Modal title="清空 Docker Hub 代理缓存" busy={busy} onClose={() => setConfirm(false)}>
          <p>将删除代理缓存和快照，下次拉取会重新回源。</p>
          <ErrorBox error={error} />
          <div className="modal-actions">
            <button className="secondary" disabled={busy} onClick={() => setConfirm(false)}>
              取消
            </button>
            <button className="danger-button" disabled={disabled} onClick={() => void clean('all')}>
              确认清空
            </button>
          </div>
        </Modal>
      )}
    </section>
  );
}
