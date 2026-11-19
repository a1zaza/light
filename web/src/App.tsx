import { useCallback, useEffect, useState, type FormEvent } from 'react';
import {
  Activity,
  ArrowRight,
  Bot,
  ChevronRight,
  ExternalLink,
  FileClock,
  Globe2,
  HardDrive,
  KeyRound,
  Layers3,
  LayoutDashboard,
  LockKeyhole,
  LogOut,
  Menu,
  Package,
  Plus,
  RefreshCw,
  Search,
  Settings2,
  ShieldCheck,
  Terminal,
  Trash2,
  X,
} from 'lucide-react';
import { MaintenancePage } from './maintenance';
import { ApplicationSettingsPanel } from './application-settings';
import { RepositoryHeader } from './repository-header';
import { api, APIError, query, registryHost, setCSRF, size, time } from './api';
import {
  Badge,
  CopyText,
  Empty,
  ErrorBox,
  Field,
  Heading,
  Loading,
  Modal,
  Pagination,
  Select,
  SelectOption,
} from './components';
import type {
  Audit,
  Detail,
  Image,
  List,
  Overview,
  Preview,
  Repository,
  Robot,
  Session,
} from './types';

type Page =
  | 'overview'
  | 'repositories'
  | 'robots'
  | 'maintenance'
  | 'audit'
  | 'settings'
  | 'public'
  | 'login';
type Route = { page: Page; name?: string };
const nav = [
  ['overview', '运行概览', LayoutDashboard],
  ['repositories', '镜像仓库', Package],
  ['robots', '机器人令牌', Bot],
  ['maintenance', '存储维护', HardDrive],
  ['audit', '操作审计', FileClock],
  ['settings', '系统设置', Settings2],
] as const;
function route(): Route {
  const [raw, ...rest] = location.hash.slice(1).split('/');
  const page = (
    [
      'overview',
      'repositories',
      'robots',
      'maintenance',
      'audit',
      'settings',
      'public',
      'login',
    ].includes(raw)
      ? raw
      : 'login'
  ) as Page;
  let name: string | undefined;
  try {
    name = rest.length ? decodeURIComponent(rest.join('/')) : undefined;
  } catch {
    name = undefined;
  }
  return { page, name };
}
function navigate(page: Page, name?: string) {
  location.hash = page + (name ? '/' + encodeURIComponent(name) : '');
}
function useData<T>(load: () => Promise<T>, dependencies: unknown[]) {
  const [data, setData] = useState<T | null>(null),
    [error, setError] = useState(''),
    [loading, setLoading] = useState(true);
  useEffect(() => {
    let active = true;
    setLoading(true);
    setError('');
    setData(null);
    load()
      .then((value) => {
        if (active) setData(value);
      })
      .catch((e) => {
        if (active) setError((e as Error).message);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, dependencies); // callers pass the complete request identity
  return { data, error, loading };
}
export function Login({ onLogin }: { onLogin: (session: Session) => void }) {
  const [username, setUsername] = useState(''),
    [password, setPassword] = useState(''),
    [busy, setBusy] = useState(false),
    [error, setError] = useState('');
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      onLogin(await api<Session>('/session', 'POST', { username, password }));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPassword('');
      setBusy(false);
    }
  }
  return (
    <div className="login-layout">
      <section className="login-story">
        <div className="brand">
          <img
            className="brand-mark"
            src="/logo.svg"
            alt=""
            aria-hidden="true"
            width={36}
            height={36}
          />
          light<span className="version-label">镜像仓库</span>
        </div>
        <div className="login-copy">
          <span className="eyebrow">让每一份镜像，都有归处。</span>
          <h1>
            你的镜像，
            <br />
            一处安放<span>。</span>
          </h1>
          <p>
            从开发到部署，从私有到公开。
            <br />
            让镜像管理回归清晰、轻盈。
          </p>
          <div className="login-features">
            <span>
              <Package size={15} />
              标准镜像推拉
            </span>
            <span>
              <ShieldCheck size={15} />
              机器人令牌
            </span>
            <span>
              <Globe2 size={15} />
              公开仓库分享
            </span>
          </div>
        </div>
        <small>light / 独立部署 · 自主管理</small>
      </section>
      <section className="login-panel">
        <form onSubmit={(e) => void submit(e)}>
          <span className="eyebrow">控制台登录</span>
          <h2>欢迎回来</h2>
          <p>使用管理员账户管理你的镜像仓库。</p>
          <Field label="管理员名称">
            <input
              autoComplete="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              disabled={busy}
              required
              maxLength={64}
            />
          </Field>
          <Field label="密码">
            <input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={busy}
              required
              maxLength={256}
            />
          </Field>
          <ErrorBox error={error} />
          <button className="primary login-submit" disabled={busy}>
            {busy ? '正在登录…' : '进入控制台'}
            <ArrowRight size={16} />
          </button>
          <div className="login-note">
            <LockKeyhole size={13} />
            仅限管理员访问，自动化请使用机器人令牌。
          </div>
          <button
            type="button"
            className="text-button public-link"
            onClick={() => navigate('public')}
          >
            <Globe2 size={14} />
            浏览公开仓库
            <ArrowRight size={13} />
          </button>
        </form>
        <small className="login-footnote">轻量镜像托管 · 数据自主保管</small>
      </section>
    </div>
  );
}
export function App() {
  const [current, setRoute] = useState<Route>(route),
    [session, setSession] = useState<Session | null | undefined>(undefined),
    [mobile, setMobile] = useState(false),
    [revision, setRevision] = useState(0),
    [error, setError] = useState(''),
    [connected, setConnected] = useState(true);
  const refresh = useCallback(() => setRevision((v) => v + 1), []);
  const login = (value: Session) => {
    setCSRF(value.csrf);
    setSession(value);
    setError('');
    navigate('overview');
  };
  useEffect(() => {
    const change = () => {
      setRoute(route());
      setMobile(false);
      setError('');
    };
    window.addEventListener('hashchange', change);
    return () => window.removeEventListener('hashchange', change);
  }, []);
  useEffect(() => {
    const expired = () => {
      setCSRF('');
      setSession(null);
      navigate('login');
    };
    window.addEventListener('light-session-expired', expired);
    api<Session>('/session')
      .then((value) => {
        setCSRF(value.csrf);
        setSession(value);
        if (route().page === 'login') navigate('overview');
      })
      .catch((e) => {
        setSession(null);
        if (!(e instanceof APIError) || e.status !== 401) setError((e as Error).message);
      });
    return () => window.removeEventListener('light-session-expired', expired);
  }, []);
  useEffect(() => {
    if (session === undefined) return;
    if (!session && current.page !== 'public' && current.page !== 'login') navigate('login');
    if (session && current.page === 'login') navigate('overview');
  }, [session, current.page]);
  useEffect(() => {
    if (!session) return;
    let active = true;
    const check = () =>
      fetch('/readyz')
        .then((r) => {
          if (active) setConnected(r.ok);
        })
        .catch(() => {
          if (active) setConnected(false);
        });
    void check();
    const timer = setInterval(() => void check(), 30000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [session]);
  async function logout() {
    try {
      await api('/session', 'DELETE');
      setCSRF('');
      setSession(null);
      navigate('login');
    } catch (e) {
      setError((e as Error).message);
    }
  }
  if (session === undefined)
    return (
      <div className="boot">
        <Loading />
      </div>
    );
  if (current.page === 'login' || (!session && current.page !== 'public'))
    return <Login onLogin={login} />;
  const isPublic = current.page === 'public';
  const title = isPublic ? '公开仓库' : nav.find((n) => n[0] === current.page)?.[1] || '镜像仓库';
  return (
    <div className="app-window">
      {mobile && (
        <button className="sidebar-mask" aria-label="收起导航" onClick={() => setMobile(false)} />
      )}
      <aside id="sidebar" className={`sidebar ${mobile ? 'open' : ''}`}>
        <div className="brand">
          <img
            className="brand-mark"
            src="/logo.svg"
            alt=""
            aria-hidden="true"
            width={36}
            height={36}
          />
          <span>
            light<small>镜像仓库控制台</small>
          </span>
        </div>
        <div className="nav-caption">{isPublic ? '镜像探索' : '功能导航'}</div>
        <nav>
          {!isPublic &&
            nav.map(([key, label, Icon]) => (
              <button
                key={key}
                className={`nav-item ${current.page === key ? 'selected' : ''}`}
                aria-current={current.page === key ? 'page' : undefined}
                onClick={() => navigate(key)}
              >
                <Icon size={17} />
                <span>{label}</span>
              </button>
            ))}
          <button
            className={`nav-item ${isPublic ? 'selected' : ''}`}
            aria-current={isPublic ? 'page' : undefined}
            onClick={() => navigate('public')}
          >
            <Globe2 size={17} />
            <span>公开仓库</span>
            {!isPublic && <ExternalLink size={12} />}
          </button>
        </nav>
        <div className="sidebar-bottom">
          {!isPublic && (
            <div className="instance-state">
              <i className={`status-dot ${connected ? 'online' : ''}`} />
              {connected ? '镜像服务运行正常' : '实例暂未就绪'}
              <span>v0.1</span>
            </div>
          )}
          <div className="account-row">
            <span className="avatar">
              {session ? session.username.slice(0, 1).toUpperCase() : <Globe2 size={16} />}
            </span>
            <div>
              {session?.username || '访客'}
              <small>{session ? '管理员' : '公开仓库只读浏览'}</small>
            </div>
            {session ? (
              <button className="icon-button" aria-label="退出登录" onClick={() => void logout()}>
                <LogOut size={16} />
              </button>
            ) : (
              <button
                className="icon-button"
                aria-label="管理员登录"
                onClick={() => navigate('login')}
              >
                <KeyRound size={16} />
              </button>
            )}
          </div>
        </div>
      </aside>
      <main className="main">
        <header className="topbar">
          <button
            className="icon-button mobile-menu"
            aria-label="打开导航"
            aria-expanded={mobile}
            aria-controls="sidebar"
            onClick={() => setMobile(true)}
          >
            <Menu size={19} />
          </button>
          <span className="breadcrumb">
            {isPublic ? '镜像探索' : '控制台'}
            <ChevronRight size={13} />
            <strong>{title}</strong>
            {current.name && (
              <>
                <ChevronRight size={13} />
                <strong className="breadcrumb-name">{current.name}</strong>
              </>
            )}
          </span>
          <div className="topbar-actions">
            <span className="muted topbar-caption">
              {isPublic ? '公开分享，自由拉取' : '个人镜像，自主掌控'}
            </span>
            <button className="icon-button" aria-label="刷新数据" onClick={refresh}>
              <RefreshCw size={15} />
            </button>
            {isPublic && (
              <button
                className="secondary small-button"
                onClick={() => navigate(session ? 'overview' : 'login')}
              >
                {session ? '管理控制台' : '管理员登录'}
                <ArrowRight size={13} />
              </button>
            )}
          </div>
        </header>
        {error && (
          <div className="alert error global-alert" role="alert">
            {error}
            <button className="icon-button" aria-label="关闭错误" onClick={() => setError('')}>
              <X size={14} />
            </button>
          </div>
        )}
        <div className="page-content" key={current.page + (current.name || '')}>
          {current.page === 'overview' && session && <OverviewPage revision={revision} />}
          {(current.page === 'repositories' || isPublic) &&
            (current.name ? (
              <RepositoryPage
                name={current.name}
                publicView={isPublic}
                revision={revision}
                onRefresh={refresh}
              />
            ) : (
              <RepositoriesPage publicView={isPublic} revision={revision} />
            ))}
          {current.page === 'robots' && session && (
            <RobotsPage revision={revision} onRefresh={refresh} />
          )}
          {current.page === 'maintenance' && session && <MaintenancePage revision={revision} />}
          {current.page === 'audit' && session && <AuditPage revision={revision} />}
          {current.page === 'settings' && session && (
            <SettingsPage
              session={session}
              revision={revision}
              onChanged={() => {
                setCSRF('');
                setSession(null);
                navigate('login');
              }}
            />
          )}
        </div>
        <footer className="statusbar">
          <span>
            <i className={`status-dot ${isPublic || connected ? 'online' : ''}`} />
            {isPublic ? '公开仓库 · 只读访问' : connected ? '已连接实例' : '实例暂未就绪'}
          </span>
          <span className="statusbar-note">
            {isPublic ? '使用 Docker 即可拉取公开镜像' : '单实例 · 本地文件存储'}
          </span>
          <span>light {isPublic ? '' : 'v0.1.0'}</span>
        </footer>
      </main>
    </div>
  );
}
function OverviewPage({ revision }: { revision: number }) {
  const { data, error, loading } = useData(() => api<Overview>('/admin/overview'), [revision]);
  return (
    <>
      <Heading
        eyebrow="一眼了解你的镜像空间"
        title="运行概览"
        description="查看镜像资产、存储容量与自动化访问情况。"
      >
        <button className="primary" onClick={() => navigate('repositories')}>
          <Package size={15} />
          查看镜像仓库
        </button>
      </Heading>
      <ErrorBox error={error} />
      {loading && <Loading />}
      {data && (
        <>
          <section className="overview-banner">
            <div className="banner-icon">
              <Layers3 size={26} />
            </div>
            <div>
              <h2>{data.ready ? '每一份镜像，井然有序。' : '实例暂未就绪'}</h2>
              <p>
                {data.ready
                  ? '镜像服务已就绪，使用机器人令牌连接你的开发与部署流程。'
                  : '请检查持久化与运行状态后恢复服务。'}
              </p>
            </div>
            <Badge color={data.ready ? 'green' : 'red'}>
              <Activity size={12} />
              {data.ready ? '服务已就绪' : '暂未就绪'}
            </Badge>
          </section>
          <div className="stats-grid">
            {[
              [Package, '镜像仓库', data.repositories, `${data.public} 个公开仓库`],
              [Layers3, '镜像标签', data.tags, '支持多架构镜像'],
              [HardDrive, '镜像文件占用', size(data.used), '镜像内容大小（含代理缓存）'],
              [Bot, '机器人令牌', data.robots, '统一推拉全部仓库'],
            ].map(([Icon, label, value, note]) => {
              const I = Icon as typeof Package;
              return (
                <div className="stat" key={String(label)}>
                  <div>
                    <span>{String(label)}</span>
                    <I size={17} />
                  </div>
                  <strong>{String(value)}</strong>
                  <small>{String(note)}</small>
                </div>
              );
            })}
          </div>
          <div className="overview-columns">
            <section className="panel">
              <div className="panel-heading">
                <h2>
                  <HardDrive size={16} />
                  存储空间
                </h2>
                <Badge>本地磁盘</Badge>
              </div>
              <div className="storage-meter">
                <div>
                  <strong>{size(data.free)}</strong>
                  <span>可用 / {size(data.total)} 卷容量</span>
                </div>
                <progress value={Math.max(0, data.total - data.free)} max={data.total || 1} />
                <small>卷已用空间包含其他文件；预留 {size(data.reserve)} 后拒绝新上传。</small>
              </div>
            </section>
            <section className="panel">
              <div className="panel-heading">
                <h2>
                  <Terminal size={16} />
                  开始使用
                </h2>
              </div>
              <div className="panel-body">
                <p>先创建机器人令牌，再使用 Docker 登录并推送镜像。</p>
                <CopyText block value={`docker login ${registryHost()}`} />
                <button className="text-button" onClick={() => navigate('robots')}>
                  管理机器人令牌
                  <ArrowRight size={14} />
                </button>
              </div>
            </section>
          </div>
          {data.auditError && <ErrorBox error={`审计写入告警：${data.auditError}`} />}
        </>
      )}
    </>
  );
}
function RepositoriesPage({ publicView, revision }: { publicView: boolean; revision: number }) {
  const [search, setSearch] = useState(''),
    [visibility, setVisibility] = useState(''),
    [page, setPage] = useState(1);
  const base = publicView ? '/public' : '/admin';
  const { data, error, loading } = useData(
    () => api<List<Repository>>(`${base}/repositories?${query({ q: search, visibility, page })}`),
    [base, search, visibility, page, revision],
  );
  return (
    <>
      <Heading
        eyebrow={publicView ? '探索可用镜像' : '你的镜像资产'}
        title={publicView ? '公开仓库' : '镜像仓库'}
        description={
          publicView
            ? '无需登录，即可浏览并拉取这里公开分享的镜像。'
            : '浏览镜像、管理标签，决定哪些仓库对外公开。'
        }
      >
        {!publicView && (
          <button className="secondary" onClick={() => navigate('robots')}>
            <Terminal size={15} />
            推送镜像
          </button>
        )}
      </Heading>
      <div className="toolbar">
        <label className="search">
          <Search size={15} />
          <input
            aria-label="搜索仓库"
            placeholder="搜索仓库名称或说明…"
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              setPage(1);
            }}
          />
        </label>
        {!publicView && (
          <Select
            aria-label="仓库可见性筛选"
            value={visibility}
            onChange={(value) => {
              setVisibility(value);
              setPage(1);
            }}
          >
            <SelectOption value="">全部可见性</SelectOption>
            <SelectOption value="private">私有仓库</SelectOption>
            <SelectOption value="public">公开仓库</SelectOption>
          </Select>
        )}
        <span className="toolbar-count">{data ? `${data.total} 个仓库` : '—'}</span>
      </div>
      <ErrorBox error={error} />
      {loading && <Loading />}
      {data &&
        (data.items.length ? (
          <>
            <div className="repository-grid">
              {data.items.map((repo) => (
                <button
                  key={repo.name}
                  className="repository-card"
                  onClick={() => navigate(publicView ? 'public' : 'repositories', repo.name)}
                >
                  <div className="card-top">
                    <span className="object-icon">
                      <Package size={19} />
                    </span>
                    <Badge color={repo.public ? 'blue' : ''}>
                      {repo.public ? <Globe2 size={11} /> : <LockKeyhole size={11} />}
                      {repo.public ? '公开' : '私有'}
                    </Badge>
                  </div>
                  <h2 title={repo.name}>{repo.name}</h2>
                  {!publicView && <Badge>自有</Badge>}
                  <p>{repo.description || '尚未填写仓库说明'}</p>
                  <div className="card-bottom">
                    <span>
                      <Layers3 size={13} />
                      {repo.tags || 0} 个标签
                    </span>
                    <span>
                      查看仓库
                      <ChevronRight size={13} />
                    </span>
                  </div>
                </button>
              ))}
            </div>
            <Pagination page={data.page} total={data.total} onChange={setPage} />
          </>
        ) : (
          <Empty
            title={
              search || visibility
                ? '没有匹配的仓库'
                : publicView
                  ? '暂无公开仓库'
                  : '还没有镜像仓库'
            }
          >
            {publicView ? (
              <p>管理员公开仓库后，将在这里展示。</p>
            ) : (
              <>
                <p>使用机器人令牌登录，首次推送后自动建立私有仓库。</p>
                <CopyText
                  block
                  value={`docker tag my-app:latest ${registryHost()}/personal/my-app:latest`}
                />
                <CopyText block value={`docker push ${registryHost()}/personal/my-app:latest`} />
              </>
            )}
          </Empty>
        ))}
    </>
  );
}
export function RepositoryPage({
  name,
  publicView,
  revision,
  onRefresh,
}: {
  name: string;
  publicView: boolean;
  revision: number;
  onRefresh: () => void;
}) {
  const [page, setPage] = useState(1),
    [search, setSearch] = useState(''),
    [detail, setDetail] = useState<Image | null>(null),
    [preview, setPreview] = useState<Preview | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState('');
  const base = publicView ? '/public' : '/admin';
  const result = useData(
    () => api<Detail>(`${base}/repository?${query({ name, q: search, page })}`),
    [name, base, search, page, revision],
  );
  const data = result.data;
  async function inspect(ref: string) {
    setError('');
    try {
      setDetail(await api<Image>(`${base}/image?${query({ name, ref })}`));
    } catch (e) {
      setError((e as Error).message);
    }
  }
  async function openDelete(tag: string) {
    setBusy(true);
    setError('');
    try {
      setPreview(await api<Preview>('/admin/delete-preview', 'POST', { name, tag }));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function remove() {
    if (!preview) return;
    setBusy(true);
    setError('');
    try {
      const result = await api<{ warning?: string }>('/admin/delete', 'POST', { id: preview.id });
      if (result.warning) setError(result.warning);
      setPreview(null);
      onRefresh();
    } catch (e) {
      setError((e as Error).message);
      setPreview(null);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <button
        className="text-button back-link"
        onClick={() => navigate(publicView ? 'public' : 'repositories')}
      >
        ← 返回仓库列表
      </button>
      <RepositoryHeader name={name} repository={data?.repository} publicView={publicView} />
      <ErrorBox error={error || result.error} />
      {result.loading && <Loading />}
      {data && (
        <>
          <section className="pull-instruction">
            <div>
              <Terminal size={16} />
              <span>{publicView ? '免登录拉取' : '拉取镜像'}</span>
            </div>
            <CopyText
              block
              value={`docker pull ${registryHost()}/${name}:${data.items[0]?.tag || 'latest'}`}
            />
          </section>
          <div className="toolbar">
            <label className="search">
              <Search size={15} />
              <input
                aria-label="搜索镜像标签"
                placeholder="搜索标签或摘要…"
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value);
                  setPage(1);
                }}
              />
            </label>
            <span className="toolbar-count">{data.total} 个标签</span>
          </div>
          {data.items.length ? (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>标签 / 内容摘要</th>
                    <th>平台</th>
                    <th title="包含全部架构的清单、配置及压缩镜像层，共享内容只计一次。">
                      逻辑大小
                    </th>
                    <th>镜像创建时间</th>
                    <th className="align-right">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((img) => (
                    <tr key={img.tag}>
                      <td>
                        <button
                          className="text-button tag-button"
                          onClick={() => void inspect(img.tag || img.digest)}
                        >
                          {img.tag}
                        </button>
                        <CopyText value={img.digest} label={img.digest.slice(0, 23) + '…'} />
                      </td>
                      <td>
                        <div className="badges">
                          {img.platforms.length ? (
                            img.platforms.map((p, i) => <Badge key={p + i}>{p}</Badge>)
                          ) : (
                            <span className="muted">未声明</span>
                          )}
                        </div>
                      </td>
                      <td>{size(img.size)}</td>
                      <td className="muted">{time(img.created)}</td>
                      <td className="align-right">
                        <div className="row-actions">
                          <button
                            className="icon-button"
                            title="查看镜像"
                            aria-label={`查看 ${img.tag}`}
                            onClick={() => void inspect(img.tag || img.digest)}
                          >
                            <Search size={15} />
                          </button>
                          {!publicView && (
                            <button
                              className="icon-button danger"
                              disabled={busy}
                              title="删除标签"
                              aria-label={`删除 ${img.tag}`}
                              onClick={() => void openDelete(img.tag!)}
                            >
                              <Trash2 size={15} />
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty title="暂无匹配标签">
              <p>此仓库可能尚无标签，或镜像已删除。</p>
            </Empty>
          )}
          <Pagination page={data.page} total={data.total} onChange={setPage} />
        </>
      )}
      {detail && (
        <Modal title="镜像详情" onClose={() => setDetail(null)}>
          {data && (
            <Field label="按摘要拉取">
              <CopyText block value={`docker pull ${registryHost()}/${name}@${detail.digest}`} />
            </Field>
          )}
          <Field label="内容摘要">
            <CopyText block value={detail.digest} />
          </Field>
          <dl className="detail-list">
            <dt>媒体类型</dt>
            <dd>{detail.mediaType}</dd>
            <dt>逻辑大小</dt>
            <dd>{size(detail.size)}（含清单、配置及压缩镜像层，共享内容只计一次）</dd>
            <dt>镜像创建时间</dt>
            <dd>{time(detail.created)}</dd>
            <dt>平台</dt>
            <dd>{detail.platforms.join('、') || '未声明'}</dd>
          </dl>
          <details>
            <summary>查看原始清单</summary>
            <pre className="raw-json">{JSON.stringify(detail.raw, null, 2)}</pre>
          </details>
        </Modal>
      )}
      {preview && (
        <Modal title="删除标签" onClose={() => setPreview(null)} busy={busy}>
          <p>
            仅删除标签 {preview.preview.tag}
            。其它标签或仓库引用的内容会保留；上传保护期内的内容将延后回收。
          </p>
          <CopyText block value={preview.preview.digest} />
          <p>
            预计释放 {size(preview.preview.bytes)}，共 {preview.preview.blobs} 个内容文件。
          </p>
          <div className="modal-actions">
            <button className="secondary" disabled={busy} onClick={() => setPreview(null)}>
              取消
            </button>
            <button className="danger-button" disabled={busy} onClick={() => void remove()}>
              {busy ? '正在删除…' : '确认删除标签'}
            </button>
          </div>
        </Modal>
      )}
    </>
  );
}
export function RobotsPage({ revision, onRefresh }: { revision: number; onRefresh: () => void }) {
  const { data, error, loading } = useData(
    () => api<{ items: Robot[] }>('/admin/robots'),
    [revision],
  );
  const [create, setCreate] = useState(false),
    [secret, setSecret] = useState<{ robot: Robot; secret: string } | null>(null),
    [pending, setPending] = useState<Robot | null>(null),
    [busy, setBusy] = useState(false),
    [actionError, setActionError] = useState('');
  async function toggle(robot: Robot) {
    setBusy(true);
    setActionError('');
    try {
      await api(`/admin/robot?${query({ id: robot.id })}`, 'PATCH', { enabled: !robot.enabled });
      onRefresh();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function revoke() {
    if (!pending) return;
    setBusy(true);
    setActionError('');
    try {
      await api(`/admin/robot?${query({ id: pending.id })}`, 'DELETE');
      setPending(null);
      onRefresh();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <Heading
        eyebrow="为你的自动化准备"
        title="机器人令牌"
        description="供 Docker、CI 与部署脚本使用，统一支持全部仓库的推送和拉取。"
      >
        <button className="primary" onClick={() => setCreate(true)}>
          <Plus size={15} />
          创建机器人令牌
        </button>
      </Heading>
      <div className="info-strip">
        <ShieldCheck size={16} />
        <span>秘密仅在创建时显示一次。机器人不用于登录管理控制台。</span>
      </div>
      <ErrorBox error={error || actionError} />
      {loading && <Loading />}
      {data &&
        (data.items.length ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>机器人 / 登录用户名</th>
                  <th>状态</th>
                  <th>到期时间</th>
                  <th>最近使用</th>
                  <th className="align-right">操作</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((robot) => {
                  const expired = !!robot.expiresAt && new Date(robot.expiresAt) <= new Date();
                  return (
                    <tr key={robot.id}>
                      <td>
                        <strong>{robot.name}</strong>
                        <CopyText value={robot.username} />
                      </td>
                      <td>
                        <Badge color={!robot.enabled || expired ? '' : 'green'}>
                          {!robot.enabled ? '已停用' : expired ? '已过期' : '有效'}
                        </Badge>
                      </td>
                      <td>{robot.expiresAt ? time(robot.expiresAt) : '永不过期'}</td>
                      <td className="muted">{time(robot.lastUsedAt)}</td>
                      <td>
                        <div className="row-actions">
                          <button
                            className="secondary small-button"
                            disabled={busy}
                            onClick={() => void toggle(robot)}
                          >
                            {robot.enabled ? '停用' : '启用'}
                          </button>
                          <button
                            className="icon-button danger"
                            disabled={busy}
                            aria-label={`撤销 ${robot.name}`}
                            onClick={() => setPending(robot)}
                          >
                            <Trash2 size={15} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty title="还没有机器人令牌">
            <p>为 CI、开发设备或部署服务器各创建一个，便于分别停用和撤销。</p>
          </Empty>
        ))}
      {create && (
        <CreateRobot
          onClose={() => setCreate(false)}
          onCreated={(value) => {
            setCreate(false);
            setSecret(value);
            onRefresh();
          }}
        />
      )}
      {secret && (
        <Modal title="保存机器人令牌" onClose={() => setSecret(null)}>
          <div className="alert">请现在复制并妥善保存秘密，关闭后无法再次查看。</div>
          <Field label="登录用户名">
            <CopyText block value={secret.robot.username} />
          </Field>
          <Field label="令牌秘密">
            <CopyText block value={secret.secret} />
          </Field>
          <p className="muted">将秘密通过标准输入传入 docker login，避免保存在命令历史中。</p>
          <div className="modal-actions">
            <button className="primary" onClick={() => setSecret(null)}>
              我已保存，关闭
            </button>
          </div>
        </Modal>
      )}
      {pending && (
        <Modal title="撤销机器人令牌" onClose={() => setPending(null)} busy={busy}>
          <p>撤销“{pending.name}”后，使用此机器人的后续推拉请求将失败。该操作无法恢复。</p>
          <ErrorBox error={actionError} />
          <div className="modal-actions">
            <button className="secondary" disabled={busy} onClick={() => setPending(null)}>
              取消
            </button>
            <button className="danger-button" disabled={busy} onClick={() => void revoke()}>
              {busy ? '正在撤销…' : '确认撤销'}
            </button>
          </div>
        </Modal>
      )}
    </>
  );
}
function CreateRobot({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (value: { robot: Robot; secret: string }) => void;
}) {
  const [name, setName] = useState(''),
    [days, setDays] = useState('90'),
    [busy, setBusy] = useState(false),
    [error, setError] = useState('');
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      onCreated(
        await api('/admin/robots', 'POST', {
          name,
          expiresAt:
            days === '0' ? null : new Date(Date.now() + Number(days) * 86400000).toISOString(),
        }),
      );
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal title="创建机器人令牌" onClose={onClose} busy={busy}>
      <form onSubmit={(e) => void submit(e)}>
        <Field label="机器人名称" hint="例如：GitHub Actions、家庭服务器。">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={40}
            required
            disabled={busy}
          />
        </Field>
        <Field label="有效期">
          <Select value={days} onChange={setDays} disabled={busy}>
            <SelectOption value="30">30 天</SelectOption>
            <SelectOption value="90">90 天</SelectOption>
            <SelectOption value="365">365 天</SelectOption>
            <SelectOption value="0">永不过期</SelectOption>
          </Select>
        </Field>
        <p className="muted">创建后可推拉全部公开与私有仓库。</p>
        <ErrorBox error={error} />
        <div className="modal-actions">
          <button className="secondary" type="button" disabled={busy} onClick={onClose}>
            取消
          </button>
          <button className="primary" disabled={busy}>
            {busy ? '正在创建…' : '创建令牌'}
          </button>
        </div>
      </form>
    </Modal>
  );
}
function AuditPage({ revision }: { revision: number }) {
  const [page, setPage] = useState(1);
  const { data, error, loading } = useData(
    () => api<List<Audit>>(`/admin/audit?${query({ page })}`),
    [page, revision],
  );
  return (
    <>
      <Heading
        eyebrow="关键操作，有迹可循"
        title="操作审计"
        description="记录账户登录、机器人变更、仓库设置、镜像删除与维护任务。"
      />
      <ErrorBox error={error} />
      {loading && <Loading />}
      {data &&
        (data.items.length ? (
          <>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>时间</th>
                    <th>操作人</th>
                    <th>操作</th>
                    <th>对象</th>
                    <th>来源地址</th>
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((a, i) => (
                    <tr key={a.time + i}>
                      <td className="nowrap muted">{time(a.time)}</td>
                      <td>{a.actor || '—'}</td>
                      <td>{a.action}</td>
                      <td className="break-word">{a.target || '—'}</td>
                      <td>{a.ip || '本机命令'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <Pagination page={data.page} total={data.total} onChange={setPage} />
          </>
        ) : (
          <Empty title="暂无操作记录" />
        ))}
    </>
  );
}
function SettingsPage({
  session,
  revision,
  onChanged,
}: {
  session: Session;
  revision: number;
  onChanged: () => void;
}) {
  const { data, error } = useData(() => api<Overview>('/admin/overview'), [revision]);
  const [username, setUsername] = useState(session.username),
    [oldPassword, setOldPassword] = useState(''),
    [password, setPassword] = useState(''),
    [confirm, setConfirm] = useState(''),
    [busy, setBusy] = useState(false),
    [actionError, setActionError] = useState('');
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      setActionError('两次新密码不一致');
      return;
    }
    setBusy(true);
    setActionError('');
    try {
      await api('/admin/password', 'POST', { username, oldPassword, password });
      onChanged();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setBusy(false);
      setOldPassword('');
      setPassword('');
      setConfirm('');
    }
  }
  return (
    <>
      <Heading
        eyebrow="你的实例，你来掌控"
        title="系统设置"
        description="管理应用配置与管理员账户安全。"
      />
      <ErrorBox error={error} />
      <section className="panel">
        <div className="panel-heading">
          <h2>
            <Settings2 size={16} />
            实例信息
          </h2>
          <Badge>实例状态</Badge>
        </div>
        <div className="panel-body">
          {data ? (
            <dl className="detail-list">
              <dt>外部访问地址</dt>
              <dd>
                <CopyText value={window.location.origin} />
              </dd>
              <dt>存储方式</dt>
              <dd>本地文件系统 · 无额外数据库</dd>
              <dt>部署方式</dt>
              <dd>单实例 · 内嵌 Registry 与控制台</dd>
              <dt>磁盘预留</dt>
              <dd>{size(data.reserve)}</dd>
              <dt>程序版本</dt>
              <dd>light {data.version}</dd>
            </dl>
          ) : (
            <Loading />
          )}
          <p className="muted">
            访问地址取自当前页面，监听端口固定为 5080；磁盘预留通过启动参数调整。
          </p>
        </div>
      </section>
      <ApplicationSettingsPanel />
      <section className="panel">
        <div className="panel-heading">
          <h2>
            <ShieldCheck size={16} />
            账户安全
          </h2>
        </div>
        <form className="panel-body settings-form" onSubmit={(e) => void submit(e)}>
          <Field label="管理员名称">
            <input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              maxLength={64}
              required
              disabled={busy}
            />
          </Field>
          <Field label="原密码">
            <input
              type="password"
              value={oldPassword}
              onChange={(e) => setOldPassword(e.target.value)}
              autoComplete="current-password"
              maxLength={256}
              required
              disabled={busy}
            />
          </Field>
          <div className="form-grid">
            <Field label="新密码" hint="至少 8 字节，建议使用长密码。">
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password"
                minLength={8}
                maxLength={256}
                required
                disabled={busy}
              />
            </Field>
            <Field label="确认新密码">
              <input
                type="password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                autoComplete="new-password"
                required
                disabled={busy}
              />
            </Field>
          </div>
          <ErrorBox error={actionError} />
          <div className="form-footer">
            <small>保存后所有管理员会话失效，请重新登录。</small>
            <button className="primary" disabled={busy}>
              {busy ? '正在保存…' : '更新账户'}
            </button>
          </div>
        </form>
      </section>
    </>
  );
}
