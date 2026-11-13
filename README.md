# light

个人 Docker/OCI 镜像仓库。Go 单程序内嵌 CNCF Distribution 和 React 中文控制台，镜像与管理数据保存在本地文件，不依赖额外数据库或 Redis。界面与架构参考 v2way。

支持公开/私有仓库、机器人令牌、多级仓库路径、多架构镜像、多标签、清单查看、单标签删除、存储维护与操作审计。面向 Linux 本地文件系统和单实例部署，同一数据目录不能同时被多个进程使用。

## Docker Compose 部署

项目提供 `deploy/Dockerfile` 与 `docker-compose.yml`，本地镜像名为 `light:local`。镜像内置控制台，以 `1000:1000` 用户运行，默认数据目录为 `/var/lib/light`，固定监听 `:5080`。Compose 将 `./data` 挂载为数据目录，端口仅映射到宿主机 `127.0.0.1:5080`。

先准备 `data` 目录，确保容器用户 `1000:1000` 可写，权限建议为 `0700`。然后在项目根目录执行：

```bash
docker compose build
docker compose run --rm --no-deps light init -username admin
docker compose up -d
```

初始化时交互设置管理员密码，要求 8–256 字节；没有固定初始密码。已有管理员不会被覆盖。非交互初始化可使用 `-password-stdin`，通过标准输入提供密码，不写入命令参数或历史。

浏览器可通过 `http://127.0.0.1:5080` 访问控制台。公网部署应由反向代理提供 HTTPS。Compose 已设置只读容器根文件系统、移除额外 capabilities 和禁止提权；管理数据与镜像内容写入数据卷。

本地 `linux/amd64` 打包已验证，详见[构建记录](docs/local-docker-build-2026-11-13.json)。远程发布由 `.github/workflows/publish-image.yml` 手动触发，目标为 GHCR，目前配置的平台为 `linux/amd64`；提供 workflow 不代表已有远程镜像可直接使用。

## 反向代理与限流 IP

可使用 v2way 或其他反向代理，将镜像域名的根路径 `/` 转发到 light。代理需要：

- 保留原始 Host、Authorization、Cookie、查询参数及 Registry 请求/响应头，包括 Accept、Range 和 Location。
- 覆盖设置正确的 `X-Forwarded-Proto`，不依赖 `X-Forwarded-Host`。
- 支持大请求与流式传输，设置合适的上传限额和空闲超时，不对所有请求使用短总超时或小正文限制。
- 不对 Registry、认证与管理 API 做共享缓存。

不配置外部 URL 或域名白名单。页面复制地址使用当前浏览器的 host/origin；认证 realm 根据请求 Host 和协议生成，上传 Location 使用相对路径。应用仍校验登录同源关系和管理写操作的 CSRF。

需要按代理后的真实客户端 IP 限流时，在启动环境中显式设置：

```text
LIGHT_RATE_LIMIT_IP_HEADER=X-Light-Client-IP
```

也可使用 `-rate-limit-ip-header X-Light-Client-IP`，启动参数优先于环境变量；Compose 已透传该环境变量。默认禁用 Header 信任，使用 TCP 连接来源 IP。

启用后，指定 Header 含唯一合法 IPv4/IPv6 地址时，限流与审计使用该值；缺失、无效、重复、包含逗号链、端口或 IPv6 zone 时回退 RemoteAddr。上层反代必须覆盖客户端传入的同名头，例如 Nginx：

```nginx
proxy_set_header X-Light-Client-IP $remote_addr;
```

启用此配置时，应限制后端只能被受控代理访问。Header 名称本身不构成认证，不应原样信任客户端传入的值。

v2way 使用 host 网络时可访问宿主回环映射；其他容器网络需使用对应的后端地址，容器内的 `127.0.0.1` 不代表另一个容器。light 的会话 Cookie 需要正常转发，不依赖 v2way 自身的管理 Cookie。

## 仓库与镜像使用

管理员登录控制台后，在“机器人令牌”创建推拉凭据。登录用户名和秘密用于 Docker 登录，秘密仅在创建时显示一次；CI 应使用 `--password-stdin` 从秘密管理工具读取。

将以下域名、机器人用户名及镜像名替换为实际值：

```bash
docker login registry.example.com --username robot-实际ID
docker tag app:latest registry.example.com/personal/app:latest
docker push registry.example.com/personal/app:latest
docker pull registry.example.com/personal/app:latest
```

首次成功推送自动建立仓库。机器人统一具有所有仓库的 pull/push 能力，不配置逐令牌权限范围；不能登录网页、调用管理 API 或直接删除已存储内容。

新仓库默认私有。管理员可在详情页直接切换公开状态、编辑备注；备注失焦后自动保存。仓库名称是推拉路径，创建后不可修改。

公开仓库支持匿名网页浏览和 `docker pull`，匿名用户不能推送。转回私有后，后续匿名请求和此前签发的匿名令牌均被拒绝；已经开始的传输及已经下载的内容无法追回。

镜像列表与详情显示平台、创建时间和逻辑大小：

- 逻辑大小包含根清单、递归子清单、配置与压缩 layer，同一引用图内按 digest 去重。
- 多架构优先读取索引自身的 `org.opencontainers.image.created` 注解；缺失或无效时取各平台配置中的最新有效创建时间。单架构取 config.created，时间缺失时显示“—”。
- 单标签删除只解绑所选 tag，保留其它标签、索引/subject 和跨仓库仍引用的内容；执行前重新核对删除预览。

## 删除、回收与上传保护

删除和 GC 共用 24 小时上传保护，避免清理“layer 已上传、manifest 尚未发布”的内容。新 blob，以及机器人携带 push scope 检查、挂载或完成上传的内容，在保护期内不回收。仅 pull scope 和匿名拉取不续租。

保护记录持久化在 `jobs/upload-leases.json`，最多保留 32768 个未过期摘要。记录写入失败时拒绝对应完成/复用请求并暂停回收，损坏时拒绝启动。首次为已有数据建立记录时，保守保护现有内容 24 小时，截止时间跨重启保持不变。

tag 删除立即生效，但受保护内容会延后释放；保护期结束后可通过维护回收。超过最后一次上传/复用检查 24 小时仍未发布的内容不再保证保留，长时间暂停的推送应重新检查或推送。预览显示当前可清理文件的逻辑体积，不承诺等量磁盘分配空间释放。

推荐通过控制台“存储维护”执行空间回收、废弃上传清理或索引重建：

1. 发起预览并检查范围，预览不删除文件。
2. 确认执行；应用暂停镜像请求，等待在途请求结束后独占存储，复查预览并清理。
3. 在任务记录查看结果，任务结束后自动恢复镜像请求；控制台与健康检查保持可用。

维护在应用进程内运行，不调用 Docker 或挂载 Docker socket。同一时间仅一个任务，预览有效 30 分钟且只能执行一次；保留最近 12 条任务。异常或重启后的未完成任务标记为中断，不自动重放删除。

GC 保留存续的无标签清单。单标签删除仅处理所选镜像及递归子清单中无剩余引用的内容，不顺带清理其它孤立数据。废弃上传需创建时间和最近活动时间都早于阈值，默认 7 天、最短 24 小时。索引重建只刷新派生列表，不删除镜像。

需要离线排障时，先正常停止服务并备份，再运行同版本命令：

```bash
docker compose stop light
docker compose run --rm --no-deps light check
docker compose run --rm --no-deps light gc -dry-run
docker compose run --rm --no-deps light gc -execute
docker compose up -d light
```

废弃上传清理使用 `purge-uploads -older-than 168h`，同样必须选择 `-dry-run` 或 `-execute`。离线命令与运行实例共用数据目录锁，不能在服务运行时对同一数据卷执行。

## 配置与备份

| 参数                     | 默认值                 | 用途                                    |
| ------------------------ | ---------------------- | --------------------------------------- |
| `-data`                  | `/var/lib/light`       | 数据目录                                |
| `-reserve-bytes`         | `1073741824`           | 可用空间低于 1 GiB 时拒绝新上传         |
| `-max-upload-bytes`      | `21474836480`          | 单次上传请求正文上限 20 GiB，0 表示不限 |
| `-transfer-idle-timeout` | `5m`                   | 上传/下载空闲超时，不是总传输时限       |
| `-rate-limit-ip-header`  | 空，或同名用途环境变量 | 信任的代理客户端 IP Header              |

监听端口固定为 `5080`，不提供 `-address`、`-external-url`。公开网页 API 按来源 IP/路径限制为每分钟 60 次、全局 4 并发和 15 秒计算预算；这些限制不等同于 Registry 大文件传输限制。

`/healthz` 用于存活检查，`/readyz` 用于就绪检查。`light health` 默认访问本机 `/readyz`，不获取实例锁；`light check` 是离线检查，不能代替运行时健康检查。

备份时正常停止实例，完整复制数据目录，包括 `management.json`、`registry/`、`jobs/` 和审计记录。`management.json` 含账户/机器人摘要及签名秘密，应按敏感数据保护。恢复时使用同版本程序检查数据，启动后重新登录、重建索引并按已知 digest 验证拉取；索引重建不能修复管理数据或镜像内容损坏。

## 本地开发

依赖 Go 1.26.11 或兼容的更新版本、Node.js 24、npm 11。首次初始化后分别运行后端和前端：

```bash
go run ./cmd/light init -data .data -username admin
go run ./cmd/light serve -data .data
```

```bash
cd web
npm ci --ignore-scripts
npm run dev -- --port 5174 --strictPort
```

开发界面为 `http://127.0.0.1:5174`，Vite 代理后端 API、Registry 与认证。`.data` 用于开发，Compose 的 `./data` 用于容器部署，均不应提交或混用。

检查命令：

```bash
go test -race ./...
go vet ./...
cd web
npm run typecheck
npm test
npm run format:check
```

单程序发布包需先在 `web/` 执行 `npm run build`，再在项目根目录编译 `cmd/light`；缺少前端产物时只提供开发提示页。Dockerfile 已包含这两个构建阶段。GitHub Actions 检查工作流覆盖 Go/前端检查，镜像发布工作流需手动触发。

## 验证范围与资料

已验证真实 Docker 推拉、多级名称、amd64/arm64 按平台拉取、多标签、跨仓库 layer mount、写入文件后推送/拉取、公开/私有切换，以及本地 linux/amd64 Dockerfile 打包。最近安全修复记录中的竞态测试、前端检查和漏洞扫描均通过；结果是对应时间的检查记录，不代表长期安全保证。

待专项验证：Buildx 多平台构建及证明材料、完整 OCI 1.1 制品兼容性、真实 HTTPS 代理、发布容器运行、浏览器视觉/移动端真机、磁盘满和断电恢复。当前内核没有原生 Referrers API，不宣称完整 OCI 1.1 兼容。

- [开发与协作约定](AGENTS.md)
- [实施状态](docs/implementation-status.md)
- [安全修复与验证](docs/security-fixes-2026-11-12.md)
- [公开/私有拉取实验](docs/dev-image-pull-visibility-results-20261112-171822.json)
- [本地 Docker 构建结果](docs/local-docker-build-2026-11-13.json)
- [变更记录](docs/changelogs/2026-11.md)
