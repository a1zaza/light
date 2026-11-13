import { useEffect, useState } from 'react';
import { Database, FileClock, HardDrive, RefreshCw, Trash2 } from 'lucide-react';
import { api, size, time } from './api';
import {
  Badge,
  Empty,
  ErrorBox,
  Field,
  Heading,
  Loading,
  Modal,
  Select,
  SelectOption,
} from './components';

type Kind = 'gc' | 'uploads' | 'reindex';
export type MaintenanceJob = {
  id: string;
  kind: Kind;
  mode: 'preview' | 'execute';
  status: string;
  stage: string;
  actor: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  expiresAt?: string;
  olderThanHours?: number;
  items: number;
  files: number;
  bytes: number;
  error?: string;
  consumed?: boolean;
};
type LegacyJob = {
  status: string;
  startedAt: string;
  finishedAt?: string;
  dryRun: boolean;
  error?: string;
};
export type MaintenanceState = {
  active: MaintenanceJob | null;
  jobs: MaintenanceJob[];
  error: string;
  catalogAt: string;
  catalogError: string;
  legacy: Record<string, LegacyJob>;
};
const kindLabel = (kind: Kind) =>
  ({ gc: '空间回收', uploads: '废弃上传清理', reindex: '索引重建' })[kind];
const statusLabel = (status: string) =>
  ({
    queued: '等待中',
    running: '进行中',
    completed: '已完成',
    failed: '失败',
    interrupted: '已中断',
  })[status] || status;
const stageLabel = (stage: string) =>
  ({
    waiting: '等待在途镜像请求结束',
    scanning: '正在扫描存储',
    cleaning: '正在校验并执行维护',
    finished: '任务已结束',
  })[stage] || stage;
function PreviewSummary({ job }: { job: MaintenanceJob }) {
  return (
    <div className="cleanup-summary">
      <div>
        <strong>{job.items}</strong>
        <span>清理项目</span>
      </div>
      <div>
        <strong>{job.files}</strong>
        <span>涉及文件</span>
      </div>
      <div>
        <strong>{size(job.bytes)}</strong>
        <span>预计清理文件体积</span>
      </div>
    </div>
  );
}
export function MaintenancePage({ revision }: { revision: number }) {
  const [data, setData] = useState<MaintenanceState | null>(null),
    [error, setError] = useState(''),
    [actionError, setActionError] = useState(''),
    [busy, setBusy] = useState(false),
    [refresh, setRefresh] = useState(0),
    [days, setDays] = useState('7'),
    [confirm, setConfirm] = useState<MaintenanceJob | null>(null);
  useEffect(() => {
    let mounted = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    async function poll() {
      let delay = 5000;
      try {
        const next = await api<MaintenanceState>('/admin/maintenance');
        if (mounted) {
          setData(next);
          setError('');
          if (next.active) delay = 1000;
        }
      } catch (e) {
        if (mounted) setError((e as Error).message);
      } finally {
        if (mounted) timer = setTimeout(() => void poll(), delay);
      }
    }
    void poll();
    return () => {
      mounted = false;
      if (timer) clearTimeout(timer);
    };
  }, [revision, refresh]);
  const disabled = busy || !!data?.active || !!data?.error;
  async function start(action: string, preview?: MaintenanceJob) {
    setBusy(true);
    setActionError('');
    try {
      const result = await api<{ job: MaintenanceJob }>('/admin/maintenance', 'POST', {
        action,
        ...(preview ? { previewId: preview.id } : {}),
        ...(action === 'uploads-preview' ? { olderThanHours: Number(days) * 24 } : {}),
      });
      setData((value) => (value ? { ...value, active: result.job } : value));
      setConfirm(null);
      setRefresh((v) => v + 1);
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  function latestPreview(kind: Kind) {
    return data?.jobs.find(
      (job) =>
        job.kind === kind &&
        job.mode === 'preview' &&
        job.status === 'completed' &&
        !job.consumed &&
        !!job.expiresAt &&
        new Date(job.expiresAt) > new Date(),
    );
  }
  return (
    <>
      <Heading
        eyebrow="让存储保持有序"
        title="存储维护"
        description="在控制台预览并执行维护，任务由当前实例完成。"
      />
      <div className="info-strip">
        <HardDrive size={16} />
        <span>
          维护期间暂停镜像推拉，等待在途请求结束后开始；控制台保持可用，任务结束后自动恢复。
        </span>
      </div>
      <ErrorBox error={error || actionError || data?.error || ''} />
      {!data && !error && <Loading />}
      {data && (
        <>
          {data.active && (
            <section className="panel" aria-live="polite">
              <div className="panel-heading">
                <h2>
                  <RefreshCw size={16} className="spin" />
                  当前维护任务
                </h2>
                <Badge color="blue">{statusLabel(data.active.status)}</Badge>
              </div>
              <div className="panel-body">
                <p>
                  {kindLabel(data.active.kind)} ·{' '}
                  {data.active.mode === 'preview' ? '仅预览' : '执行'} ·{' '}
                  {stageLabel(data.active.stage)}
                </p>
                <p className="muted">
                  切换页面或关闭浏览器不会取消任务。开始时间：
                  {time(data.active.startedAt || data.active.createdAt)}
                </p>
              </div>
            </section>
          )}
          <div className="maintenance-grid">
            {(['gc', 'uploads'] as const).map((kind) => {
              const preview = latestPreview(kind);
              const ready =
                !!preview &&
                preview.items > 0 &&
                (kind !== 'uploads' || preview.olderThanHours === Number(days) * 24);
              return (
                <section className="panel" key={kind}>
                  <div className="panel-heading">
                    <h2>
                      {kind === 'gc' ? <HardDrive size={16} /> : <Trash2 size={16} />}{' '}
                      {kindLabel(kind)}
                    </h2>
                    <Badge>先预览后执行</Badge>
                  </div>
                  <div className="panel-body">
                    <p>
                      {kind === 'gc'
                        ? '回收不再被清单引用的镜像文件。保留无标签清单、多架构子镜像和共享层。'
                        : '清理超过指定时间没有活动的未完成上传，释放临时文件。'}
                    </p>
                    {kind === 'uploads' && (
                      <div className="maintenance-age">
                        <Field label="最短闲置时间">
                          <Select
                            aria-label="上传最短闲置时间"
                            value={days}
                            disabled={disabled}
                            onChange={setDays}
                          >
                            <SelectOption value="1">1 天</SelectOption>
                            <SelectOption value="7">7 天</SelectOption>
                            <SelectOption value="30">30 天</SelectOption>
                          </Select>
                        </Field>
                      </div>
                    )}
                    {preview ? (
                      <>
                        <PreviewSummary job={preview} />
                        <p className="muted">
                          预览时间：{time(preview.finishedAt)}
                          {kind === 'uploads'
                            ? ` · 闲置至少 ${(preview.olderThanHours || 0) / 24} 天`
                            : ''}
                          。预览结果有效 30 分钟，执行前会再次核对。
                        </p>
                        {preview.items === 0 && <p className="muted">当前没有可清理的项目。</p>}
                      </>
                    ) : (
                      <p className="maintenance-placeholder">
                        先运行预览，查看清理范围与文件体积。
                      </p>
                    )}
                    <div className="maintenance-actions">
                      <button
                        className="secondary"
                        disabled={disabled}
                        onClick={() => void start(`${kind}-preview`)}
                      >
                        <RefreshCw size={14} />
                        预览{kind === 'gc' ? '空间回收' : '废弃上传'}
                      </button>
                      <button
                        className="danger-button"
                        disabled={disabled || !ready}
                        onClick={() => setConfirm(preview!)}
                      >
                        {kind === 'gc' ? '执行空间回收' : '执行上传清理'}
                      </button>
                    </div>
                  </div>
                </section>
              );
            })}
          </div>
          <section className="panel">
            <div className="panel-heading">
              <h2>
                <Database size={16} />
                仓库索引
              </h2>
              <Badge>可重建</Badge>
            </div>
            <div className="panel-body">
              <p>索引更新：{time(data.catalogAt)}。重新扫描镜像列表，不改变仓库权限或删除镜像。</p>
              <ErrorBox error={data.catalogError} />
              <div className="maintenance-actions">
                <button
                  className="secondary"
                  disabled={disabled}
                  onClick={() => void start('reindex')}
                >
                  <RefreshCw size={14} />
                  重建仓库索引
                </button>
              </div>
            </div>
          </section>
          <section className="panel">
            <div className="panel-heading">
              <h2>
                <FileClock size={16} />
                维护任务记录
              </h2>
              <Badge>最近 12 条</Badge>
            </div>
            {data.jobs.length ? (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>任务</th>
                      <th>状态</th>
                      <th>目标项目 / 文件体积</th>
                      <th>发起人</th>
                      <th>完成时间</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.jobs.map((job) => (
                      <tr key={job.id}>
                        <td>
                          {kindLabel(job.kind)}
                          <small className="job-mode">
                            {job.mode === 'preview' ? '预览' : '执行'}
                          </small>
                        </td>
                        <td>
                          <Badge
                            color={
                              job.status === 'completed'
                                ? 'green'
                                : job.status === 'failed' || job.status === 'interrupted'
                                  ? 'red'
                                  : 'blue'
                            }
                          >
                            {statusLabel(job.status)}
                          </Badge>
                          {job.error && <p className="job-error">{job.error}</p>}
                        </td>
                        <td>
                          {job.kind === 'reindex' ? '—' : `${job.items} / ${size(job.bytes)}`}
                        </td>
                        <td>{job.actor}</td>
                        <td className="muted">{time(job.finishedAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <Empty title="暂无维护任务">
                <p>发起预览或索引重建后，任务进度和结果会显示在这里。</p>
              </Empty>
            )}
          </section>
          {Object.keys(data.legacy).length > 0 && (
            <section className="panel">
              <div className="panel-heading">
                <h2>
                  <FileClock size={16} />
                  离线维护记录
                </h2>
              </div>
              <div className="panel-body">
                {Object.entries(data.legacy).map(([kind, job]) => (
                  <div key={kind}>
                    <p>
                      {kind === 'gc' ? '空间回收' : '废弃上传清理'} · {job.dryRun ? '预览' : '执行'}{' '}
                      · {statusLabel(job.status)} · {time(job.finishedAt || job.startedAt)}
                    </p>
                    <ErrorBox error={job.error || ''} />
                  </div>
                ))}
              </div>
            </section>
          )}
        </>
      )}
      {confirm && (
        <Modal
          title={`确认${kindLabel(confirm.kind)}`}
          onClose={() => setConfirm(null)}
          busy={busy}
        >
          <p>将清理以下预览中的文件，执行时暂停镜像推拉，完成后自动恢复。此操作无法撤销。</p>
          <PreviewSummary job={confirm} />
          <p className="muted">如果预览后清理范围发生变化，任务会停止并要求重新预览。</p>
          <ErrorBox error={actionError} />
          <div className="modal-actions">
            <button className="secondary" disabled={busy} onClick={() => setConfirm(null)}>
              取消
            </button>
            <button
              className="danger-button"
              disabled={disabled}
              onClick={() => void start(`${confirm.kind}-execute`, confirm)}
            >
              {busy ? '正在提交…' : '确认执行'}
            </button>
          </div>
        </Modal>
      )}
    </>
  );
}
