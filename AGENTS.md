# 项目概述

- `light` 是个人 Docker/OCI 镜像仓库，采用 Go 单程序、内嵌 React/TypeScript 中文控制台、本地文件持久化；不依赖额外数据库或 Redis，仅支持单实例本地 Linux 文件系统。
- Registry 固定使用 CNCF Distribution `v3.1.2`，通过 `internal/registry` 适配。镜像读写、删除与清理由内核 API/Vacuum 完成，不直接修改内核存储路径或依赖源码。
- 架构和界面参考相邻项目 `../v2way`。相邻项目仅只读参考，不修改其文件、不读取或复制其运行数据，遵守资源许可证。
- 新仓库默认私有；公开仓库支持匿名浏览和拉取，匿名用户不能推送。机器人统一推拉全部仓库，不实现逐令牌权限范围；管理操作仅允许管理员会话；管理员账号不能用于 docker login，Registry 认证仅接受机器人凭据。
- 仓库名称决定推拉路径，创建后不可修改。详情顶部为只读名称、备注输入栏及其右侧公开/私有开关，不提供设置弹窗或名称/拉取权限说明。
- 项目 GitHub 地址为 `https://github.com/a1zaza/light`，origin 为 `git@github.com:a1zaza/light`，默认分支 main；发布镜像路径为 `ghcr.io/a1zaza/light`。
- Docker Hub 私有代理缓存已接入，默认关闭，在页面“系统设置 → 应用配置”启用，保存即时生效；配置与边界见 README 和 `docs/docker-hub-proxy-cache-design.md`。仅缺少仓库/tag/摘要时回源，自有引用优先；机器人认证必需，公开自有仓库的代理部分也不得匿名访问。清单每次重新获取，blob 完整校验提交后固定缓存 168 小时，不因读取续期。
- 项目入口见 `README.md`，详细部署与维护说明见 `docs/operations.md`；功能与验证边界见 `docs/implementation-status.md`；历史记录保留在 `docs/changelogs/` 和独立实验报告，不在本文件追加操作流水。

# 开发环境

## 工具与目录

- 项目最低 Go 版本为 `1.26.11`，前端使用 Node.js 24、npm 11；依赖由 `go.mod`、`go.sum`、`web/package-lock.json` 管理。Go 自动工具链下载在模块缓存中完成，不替换系统 Go。
- `cmd/light`：CLI 和进程入口；`internal/server`：管理/公开 API 与会话；`internal/registry`：Registry、引用扫描和回收适配；`internal/store`、`internal/jsonfile`：管理持久化；`internal/maintenance`：维护任务；`internal/requesturl`：请求地址与同源校验；`internal/hubproxy`：受控 Hub 客户端、独立缓存与到期清理；`internal/config`：应用设置默认值与校验；`internal/storageutil`：缺失遍历根目录的适配；`web/`：前端及内嵌资源。
- `.data` 是开发数据目录；Compose 将 `./data` 挂载到容器 `/var/lib/light`。两者不混用，不提交数据、凭据、依赖或构建产物。
- 品牌主资源为 `web/public/logo.svg`（原创矢量镜像层与光束图形），登录页、侧栏、favicon、开发提示页及 README 共用；Vite 复制 public，Go 内嵌 public 资源以支持无 dist 时的提示页，Docker 后端构建阶段必须复制 web/public。
- 字体顺序沿用 v2way：`Consolas, 'LXGW WenKai', monospace`；其 lxgwzk.woff2 的实际内部字体为霞鹜臻楷（LXGW ZhenKai），原样放在 web/public/fonts 并附带 SIL OFL 1.1 和来源说明。Consolas 仅通过 local() 使用设备已安装字体，不分发未确认再分发授权的二进制。主页面与提示页共用 public/fonts.css，表单、代码文本继承全局字体；Go public 资源处理须支持 CSS、字体和许可证文件。
- 保留 `web/dist/.keep`。单程序发布需先构建前端再编译 Go；没有前端产物时后端提供提示页，开发界面通过 Vite 访问。

## 启动与检查

- 首次直接运行 `go run ./cmd/light serve -data .data`，无账号时自动创建 `admin` 和 32 字符随机密码；持有实例锁并确认持久化成功后，初始凭据仅输出到启动控制台一次。Compose 直接 `up -d`，通过 `docker compose logs light` 获取凭据，无需单独执行 init。
- 可选保留离线 `light init`，用于首次启动前自行设置管理员名称/密码；非交互使用 `-password-stdin`。已有账号始终复用，不重置或再次输出密码；离线维护命令不触发自动初始化。
- 后端：`go run ./cmd/light serve -data .data`，固定监听 `:5080`；前端：在 `web/` 执行 `npm ci --ignore-scripts` 和 `npm run dev -- --port 5174 --strictPort`，入口为 `http://127.0.0.1:5174`。
- 后端检查：根目录 `go test -race ./...`、`go vet ./...`。前端检查：在 `web/` 执行 `npm run typecheck`、`npm test`、`npm run format:check`，不在根目录执行 npm 脚本。
- 未经明确要求，不启动开发环境、不执行 Docker build、不自动运行发布构建。已有会话授权仅适用于其明确范围，文档中的命令不是执行授权。
- 构建使用 `deploy/Dockerfile`，本地命令为 `docker build -f deploy/Dockerfile -t light:local .`。Compose 默认使用 `${LIGHT_IMAGE:-ghcr.io/a1zaza/light:latest}`，不包含 build 配置；本地构建部署需设置 `LIGHT_IMAGE=light:local`。发布工作流通过 matrix 在 amd64/arm64 原生 runner 分别构建并按 digest 推送，汇总两份摘要后生成多架构 latest/完整提交 SHA 标签，Git push 不授权也不自动执行镜像发布。已验证 linux/amd64 本地打包，详见 `docs/local-docker-build-2026-11-13-final.json`；本轮已配置双架构 matrix 与合并校验，实际 arm64 构建/运行及远程发布仍需分别验收。

## 界面约定

- 备注仅在失焦且变化时保存，公开开关立即保存；两者通过串行队列和最新 revision 合并，避免覆盖。保存期间保留新输入，失败保留备注并可重试，公开状态失败回退。不显示“已保存”及空反馈行；匿名浏览只读。
- 统一使用 `web/src/select.tsx` 的 `Select`/`SelectOption`，从 `components.tsx` 导出，不新增原生下拉。`onChange` 接收选项字符串；Portal 挂载到当前弹窗或 body，保留键盘、焦点和无障碍关联。
- 所有复制入口使用 `CopyText` 与 `clipboard.ts`。显示单行省略但复制完整 value；Clipboard API 不可用/拒绝时回退临时 textarea 与 execCommand，节点位于当前弹窗内，结束后清理并恢复焦点/选区。失败明确提示手动复制。
- 原始清单不创建独立高度限制或滚动区，由 modal-body 统一滚动。

# 测试账号

- 无固定测试密码；自动测试使用临时目录和合成凭据，不依赖 Docker daemon。凭据、签名秘密和机器人秘密不得写入文档或日志。用户明确要求的唯一例外是首次 serve 自动创建管理员后，将生成的用户名和初始密码输出一次到启动控制台；不得写入审计、管理快照明文、凭据文件或后续启动日志。测试仅捕获临时 fixture 的输出，失败报告也不打印密码。
- 开发目录 `.data` 已初始化管理员 `admin`，密码由用户管理且已修改。不得重置、覆盖账户或尝试旧初始密码；需要认证时使用会话内已授权的有效凭据。
- 密码要求为 8–256 字节；机器人秘密仅创建时显示一次，后续由用户管理停用、撤销和到期。
- 真实 Docker 测试采用独立临时认证配置，结束后清理凭据、临时容器与本轮本地标签。默认不覆盖已有远端标签，不删除开发环境已有镜像；需删除或故障注入的验证只用临时 fixture。
- 推送工具 `tools/seed-dev-images.py` 支持 `--name-prefix retest-<时间>- --docker-config <临时目录> --output <独立结果文件>`，仍拒绝覆盖已有标签。开发仓库清单、摘要及文件验证结果保存在 `docs/dev-image-*.json`，不在此复制明细。
- 真实 Docker 按非宿主平台核对时，`docker image inspect` 必须显式指定 `--platform`。需运行测试容器时使用非 root、最小权限和适当网络隔离，不能通过 Docker 绕过宿主权限限制。

# 常见问题

## 认证、地址与限流

- 默认数据目录 `/var/lib/light`，监听固定为 `:5080`，没有 `-address`、`-external-url` 或外部域名白名单。页面复制命令使用 `window.location.host/origin`；认证 realm 取请求 Host/协议，上传 Location 为相对路径，JWT issuer 固定为 light。
- 反代须保留 Host、认证头与 light Cookie，覆盖 X-Forwarded-Proto，不依赖 X-Forwarded-Host，不做 Registry/API 共享缓存。协议实际写响应头时须保持 `Cache-Control: no-store`。
- 自定义限流 IP Header 在系统设置中填写，保存即时生效，默认禁用。唯一合法 IP 值用于限流/审计，缺失或非法时回退 RemoteAddr；反代必须覆盖该头且后端只能由受控代理访问，不直接信任 X-Forwarded-For。
- 管理员认证返回实际校验的账户 revision，会话只能绑定该版本，不能再次读取新 revision 升级旧密码授权。管理写入保留同源与 CSRF 校验。
- 机器人撤销与公开转私有均逐请求查询事实数据，不能依赖派生索引授权；已经开始的传输不保证立即中止。
- 公开详情先按 tag/摘要筛选分页，再计算当前页元数据。公开 API 每 IP/路径 60 次/分钟、全局 4 并发和 15 秒计算预算，与大文件传输限制分开。

## 持久化与维护

- `management.json` 是有界原子管理快照，上限 8 MiB，保存账户/机器人摘要、仓库元数据和签名秘密；所有管理修改通过 `Store.Change`。持久化状态不确定时暂停写入。
- 数据目录实例锁由运行实例和离线命令共用。运行时使用 `light health`、`/healthz`、`/readyz`；`light check`、离线 GC 和上传清理必须正常停机后执行。
- 在线维护在应用进程内异步执行，不调用 Docker 或挂载 Docker socket。先关闭镜像请求准入，再等待 Gate 独占；控制台和健康检查可用，同一时间仅一个任务。
- 维护预览通过拦截官方收集器的删除调用获取候选，不实际删除。执行前核对范围指纹并限制目标；有效 30 分钟、单次使用，保留最近 12 条任务，单份预览最多 2 MiB。异常/重启不重放删除。
- `Server.Close` 取消并等待维护任务后，进程才释放数据目录锁。维护记录损坏/写入失败阻止新维护任务，不直接阻断镜像服务。
- 删除以 name/tag 为单位，只解绑选中 tag；同仓库其它 tag、索引/subject、跨仓库引用均保护共享内容。只处理目标镜像图，不调用全局 GC 顺带删除无关内容；执行前复查预览。
- tag 已解绑而后续清理失败时返回成功和 warning，页面刷新并显示反馈，遗留内容稍后维护回收；不能误报为 tag 未删除。
- 删除与 GC 共用 24 小时上传保护：新 blob 的文件时间及 push scope 下 HEAD、mount、完成上传前的持久化租约共同保护内容。仅 pull/匿名读取不续租。
- 租约存于 `jobs/upload-leases.json`，最多 32768 项。写入失败时拒绝对应完成/复用并暂停回收，损坏时拒绝启动；首次迁移保护现有内容 24 小时，期限跨重启不延长。tag 立即解绑，保护期内内容由到期后的维护回收，不保证立即释放磁盘。
- GC 保留存续无标签清单。废弃上传需创建和最近活动均早于阈值，默认 7 天、最短 24 小时。不启用 blob 描述符内存缓存，避免回收后 HEAD 命中过期结果。
- Registry 每进程仅创建一次，不以反复创建/销毁内核模拟重载。审计失败只告警；启动只修剪最新审计文件的不完整尾行，中间损坏停止追加并提示离线检查。
- 索引是派生数据，写入/删除后失效，读取刷新，30 秒有效；可重建但不能修复管理快照或镜像内容。完整备份应停机复制整个数据目录并保护其中的秘密。

## 应用设置

- `management.json` 的可选 `settings` 保存限流 IP Header 与 5 项 Hub 配置，有独立 revision；无此字段时直接使用代码默认值，不读取、导入或迁移环境变量。`-rate-limit-ip-header` 已移除，Compose 不填写应用配置。
- `/api/admin/settings` GET/PATCH 仅允许管理员会话，写入校验同源、CSRF 和设置版本；配置版本独立于账户、机器人、仓库修订。失败保留编辑，冲突需显式重新读取。
- 保存即时生效：先准备必要的禁用缓存实例，再持久化设置，成功后更新 Hub 策略与原子配置快照；失败不发布新策略。设置写入与 Registry 关闭串行，不能在停止期间创建新缓存。Header 通过不可变原子快照读取，不修改正在并发读取的字符串。
- Registry App 始终只创建一次；Hub 缓存第一次启用时创建，关闭只停止新代理请求，保留实例与已有缓存；再次启用复用。并发用共享在途计数管理，禁止替换正在使用的槽 channel。新超时只作用于新请求，降低容量/单 blob 上限不取消已经预留的下载，也不提前淘汰未过期对象。
- API 返回 settings 和 effective，保存成功后立即一致；页面显示即时生效。容量以整数字节持久化，页面用 GiB 输入；超时 1–3600 整数秒，单 blob 最大 20 GiB、总容量最大 1 PiB、并发 1–32、Header 最长 128 字节。`LIGHT_IMAGE` 与 `TZ` 仍属于部署层；磁盘预留、上传限制等现有 CLI 参数不在本次迁移范围。

## Docker Hub 缓存

- 缓存仅存于独立 `hub-cache/registry/`，原始清单字节以 base64 保存在 `hub-cache/metadata/state.json`，避免 JSON 格式化改变摘要。快照最多 8 MiB、1024 份清单，blob 记录最多 16384；不得直接改内核存储路径或把缓存混入自有 Namespace。
- 过期逻辑立即失效，每 5 分钟扫描；请求填充前也清理到期对象。清理排空在途请求，移除 blob 仓库链接和全局内容；异步手动清理入口在存储维护页，与自有 GC 分开，保留审计。清单快照最多保留 7 天，GET/HEAD 每次仍回源，不允许失败回退旧索引。
- 元数据损坏/持久化失败只暂停代理。恢复需正常停机并移走整个 `hub-cache/`，重启自动重建；不能只移走 metadata。缓存关闭会取消上游任务、等待清理与传输后退出。
- 上游仅匿名拉取公开 Hub 内容，固定 HTTPS 主机和 CDN 清单、直连公共解析 IP，不跟随任意 realm/跳转，不使用环境代理，不转发 light 凭据；外部层 URL 和非 SHA256 内容明确拒绝。域名变更需核对 Docker 官方 allowlist 后调整并测试，不能扩大到任意 R2 域名。
- 缓存清单/来源和计量集中在存储维护页，自有仓库列表不混入代理条目；页面不触发额外下载。自动测试使用临时存储和受控 HTTP transport；真实 Hub 多架构拉取、CDN、容器重启仍需专项验收。

## 镜像元数据与验证边界

- 首页镜像文件占用通过 Distribution BlobEnumerator/BlobStatter 统计已提交 blob，自有存储及独立代理缓存分别按摘要计数；不按仓库/标签重复累计，不扫描目录下所有文件直接求和。管理 JSON、缓存清单快照、链接与未完成上传均排除；空代理元数据 state.json 的 53 字节不能显示为镜像占用。已提交无标签 blob 及关闭代理后保留的 blob 仍计入，卷可用容量继续使用 Statfs。

- 逻辑大小递归统计清单、配置和压缩 layer，按 digest 去重；不沿 subject 回指累加，缺失内容返回错误。同摘要多标签只在当前请求内复用计算结果，不跨请求缓存大小。
- 多架构创建时间优先使用索引自身 `org.opencontainers.image.created` 有效注解，缺失/无效时取各平台 config.created 的最新有效值；单架构取 config.created。统一输出 UTC，缺失/无效/零值留空，不用推送时间或 mtime 替代。
- 内核 References() 不含原始 OCI subject，引用保护需额外解析。当前没有原生 Referrers API，不宣称完整 OCI 1.1 兼容。
- 安全问题修复与对应时间的竞态/前端/漏洞扫描记录见 `docs/security-fixes-2026-11-12.md`；原审查报告保留为历史证据，不能据此认定当前问题仍未修复，也不能把一次扫描当作长期保证。
- 已验证真实 Docker 多级路径、双架构拉取、多标签、layer mount、写入文件再推拉、公开/私有切换及本地 Dockerfile 打包。Buildx 多平台/证明材料、HTTPS 代理、发布容器运行、视觉/真机、磁盘满和断电仍需专项验收。

# 其他备忘

- 回复使用中文，并先称呼会话中获取的 Git 用户名；每会话最多获取一次。前端文案使用中文，回复不粘贴大段代码。
- 未明确要求不得执行 Git 操作。“提交变更”表示创建 commit 并 push；每次 commit 前记录 `docs/changelogs/YYYY-MM.md`，每条包含时间戳。插件明确要求的 commit 可执行，但不因此授权 push。
- 明确要求设计方案时，默认写入 `docs/<需求简写>.md`；仅要求编写 PLAN 时不实施业务变更。
- 不使用 sudo 或管理员权限，不用 Docker 绕过权限限制。终止进程前先通过 netstat 获取 PID 并核实归属，不使用 killall；需要系统权限的操作由用户执行。
- 除项目开发依赖外不自行安装软件；构建、启动、远程发布按用户授权范围执行，不因存在文档命令或 workflow 自动运行。
- 有价值的新约定、启动/故障处理知识应更新本文件；明细实验、时间记录和镜像摘要放在 docs 的独立报告中并链接，避免重新累积流水。
