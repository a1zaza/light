import { useEffect, useState, type FormEvent } from 'react';
import { api, size } from './api';
import { Badge, ErrorBox, Field, Loading } from './components';

export type ApplicationSettings = {
  revision: number;
  rateLimitIPHeader: string;
  hubEnabled: boolean;
  hubCacheMaxBytes: number;
  hubMaxBlobBytes: number;
  hubFetchConcurrency: number;
  hubFetchTimeoutSeconds: number;
};
export type SettingsResponse = {
  settings: ApplicationSettings;
  effective: ApplicationSettings;
};
const gib = 1024 ** 3;
const draftOf = (s: ApplicationSettings) => ({
  header: s.rateLimitIPHeader,
  enabled: s.hubEnabled,
  capacity: String(s.hubCacheMaxBytes / gib),
  blob: String(s.hubMaxBlobBytes / gib),
  concurrency: String(s.hubFetchConcurrency),
  timeout: String(s.hubFetchTimeoutSeconds),
});
export function ApplicationSettingsPanel() {
  const [data, setData] = useState<SettingsResponse | null>(null),
    [draft, setDraft] = useState<ReturnType<typeof draftOf> | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(''),
    [reload, setReload] = useState(0),
    [saved, setSaved] = useState(false);
  useEffect(() => {
    let active = true;
    setBusy(true);
    api<SettingsResponse>('/admin/settings')
      .then((next) => {
        if (active) {
          setData(next);
          setDraft(draftOf(next.settings));
          setSaved(false);
          setError('');
        }
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      })
      .finally(() => {
        if (active) setBusy(false);
      });
    return () => {
      active = false;
    };
  }, [reload]);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!data || !draft) return;
    const value: ApplicationSettings = {
      revision: data.settings.revision,
      rateLimitIPHeader: draft.header,
      hubEnabled: draft.enabled,
      hubCacheMaxBytes: Math.round(Number(draft.capacity) * gib),
      hubMaxBlobBytes: Math.round(Number(draft.blob) * gib),
      hubFetchConcurrency: Number(draft.concurrency),
      hubFetchTimeoutSeconds: Number(draft.timeout),
    };
    if (
      [
        value.hubCacheMaxBytes,
        value.hubMaxBlobBytes,
        value.hubFetchConcurrency,
        value.hubFetchTimeoutSeconds,
      ].some((v) => !Number.isSafeInteger(v) || v < 1)
    ) {
      setError('请填写有效的容量、整数并发数和整数秒数。');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const next = await api<SettingsResponse>('/admin/settings', 'PATCH', value);
      setData(next);
      setDraft(draftOf(next.settings));
      setSaved(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const dirty =
    !!data && !!draft && JSON.stringify(draft) !== JSON.stringify(draftOf(data.settings));
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2>应用配置</h2>
        <Badge>即时生效</Badge>
      </div>
      {!draft || !data ? (
        <div className="panel-body">
          <ErrorBox error={error} />
          {busy ? (
            <Loading />
          ) : (
            <button className="secondary" onClick={() => setReload((v) => v + 1)}>
              {dirty ? '丢弃修改并重新读取' : '重新读取配置'}
            </button>
          )}
        </div>
      ) : (
        <form className="panel-body settings-form" onSubmit={(e) => void submit(e)}>
          <p className="muted">
            配置保存在当前实例，保存后即时生效。新超时作用于后续请求，已开始的传输继续完成。
          </p>
          {saved && !dirty && <p role="status">配置已生效。</p>}
          <Field
            label="限流客户端 IP Header"
            hint="留空使用连接来源 IP。上层代理需覆盖此头，并限制后端仅允许受控代理访问。"
          >
            <input
              value={draft.header}
              maxLength={128}
              disabled={busy}
              placeholder="例如 X-Light-Client-IP"
              onChange={(e) => setDraft({ ...draft, header: e.target.value })}
            />
          </Field>
          <div className="application-switch-row">
            <div>
              <strong>Docker Hub 私有代理</strong>
              <p className="muted">本地缺少镜像引用时回源，代理内容始终需要机器人认证。</p>
            </div>
            <button
              type="button"
              role="switch"
              aria-label="启用 Docker Hub 代理"
              aria-checked={draft.enabled}
              className="visibility-switch"
              disabled={busy}
              onClick={() => setDraft({ ...draft, enabled: !draft.enabled })}
            >
              <span />
            </button>
          </div>
          <div className="form-grid">
            <Field
              label="缓存容量（GiB）"
              hint="完整 blob 与在途填充的总预算；达到容量上限时拒绝新增缓存。"
            >
              <input
                type="number"
                min="0"
                max="1048576"
                step="any"
                required
                value={draft.capacity}
                disabled={busy}
                onChange={(e) => setDraft({ ...draft, capacity: e.target.value })}
              />
            </Field>
            <Field label="单个 blob 上限（GiB）" hint="最大 20 GiB，同时受缓存总容量限制。">
              <input
                type="number"
                min="0"
                max="20"
                step="any"
                required
                value={draft.blob}
                disabled={busy}
                onChange={(e) => setDraft({ ...draft, blob: e.target.value })}
              />
            </Field>
            <Field label="回源并发数" hint="清单请求与 blob 填充共用，范围 1–32。">
              <input
                type="number"
                min="1"
                max="32"
                step="1"
                required
                value={draft.concurrency}
                disabled={busy}
                onChange={(e) => setDraft({ ...draft, concurrency: e.target.value })}
              />
            </Field>
            <Field label="回源超时（秒）" hint="范围 1–3600 秒；blob 固定缓存 7 天。">
              <input
                type="number"
                min="1"
                max="3600"
                step="1"
                required
                value={draft.timeout}
                disabled={busy}
                onChange={(e) => setDraft({ ...draft, timeout: e.target.value })}
              />
            </Field>
          </div>
          <ErrorBox error={error} />
          <div className="form-footer">
            <button
              type="button"
              className="secondary"
              disabled={busy}
              onClick={() => {
                setReload((v) => v + 1);
              }}
            >
              {dirty ? '丢弃修改并重新读取' : '重新读取配置'}
            </button>
            <button className="primary" disabled={busy || !dirty}>
              {busy ? '正在保存…' : '保存设置'}
            </button>
          </div>
          <details className="application-effective">
            <summary>当前生效配置</summary>
            <dl className="detail-list">
              <dt>限流客户端 IP</dt>
              <dd>{data.effective.rateLimitIPHeader || '连接来源 IP'}</dd>
              <dt>Docker Hub 代理</dt>
              <dd>{data.effective.hubEnabled ? '已启用' : '已关闭'}</dd>
              <dt>缓存容量</dt>
              <dd>{size(data.effective.hubCacheMaxBytes)}</dd>
              <dt>单个 blob 上限</dt>
              <dd>{size(data.effective.hubMaxBlobBytes)}</dd>
              <dt>回源并发数</dt>
              <dd>{data.effective.hubFetchConcurrency}</dd>
              <dt>回源超时</dt>
              <dd>{data.effective.hubFetchTimeoutSeconds} 秒</dd>
            </dl>
          </details>
        </form>
      )}
    </section>
  );
}
