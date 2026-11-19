# 部署与运维

本文补充 [README](../README.md) 的部署细节、配置、代理与存储维护。

## 部署约定

- 面向 Linux 本地文件系统和单实例部署，同一数据目录只能由一个进程使用。
- 容器以 `1000:1000` 运行；宿主机数据目录需允许该用户读写，权限建议为 `0700`。不挂载 Docker socket。
- Compose 将 `./data` 挂载到 `/var/lib/light`，默认仅映射到宿主机 `127.0.0.1:5080`。容器根文件系统只读，capabilities 已移除，禁止提权。
- 首次 `serve` 在持有数据目录独占锁后自动创建管理员 `admin`，生成 32 字符随机密码；持久化成功后才将凭据输出到启动控制台。容器部署通过 `docker compose logs light` 查看，直接运行时查看终端输出。
- 初始密码只在创建账号时输出一次；后续启动不重置、不再次输出已有账号的密码，已改名或改密的账号保持原样。登录后可在系统设置修改，密码要求 8–256 字节。
- 持久化仅保存密码摘要，审计不包含密码；初始明文只存在于首次启动控制台输出，不另写凭据文件。请保存首次启动日志中的凭据。
- 发布镜像为 `ghcr.io/a1zaza/light`，发布流程支持 `linux/amd64` 和 `linux/arm64`。私有 GHCR 包需先通过 `docker login ghcr.io` 认证。

### 可选离线初始化

仍可在首次启动前使用 `light init` 自行设置管理员名称和密码；此时启动会复用已初始化账号。非交互场景使用 `-password-stdin`，不把密码写入参数或历史。该命令不会覆盖已有账号，也不能用来重置密码。

`check`、`gc`、`purge-uploads` 不会自动创建账号；未初始化时提示先启动 `light serve`。初始化失败不输出初始凭据、不启动服务；控制台输出失败会报告错误，已成功持久化的账号不会被重置。

### 更新镜像

使用远程镜像时：

```bash
docker compose pull
docker compose up -d
```

使用本地源码时，在后续 Compose 命令所在终端保持相同的 `LIGHT_IMAGE`：

```bash
docker build -f deploy/Dockerfile -t light:local .
export LIGHT_IMAGE=light:local
docker compose up -d
```

启动自动完成首次初始化，更新时保留整个数据目录。可以用 `LIGHT_IMAGE=ghcr.io/a1zaza/light:完整提交SHA` 或 `ghcr.io/a1zaza/light@sha256:摘要` 固定版本。

[检查工作流](../.github/workflows/check.yml) 在 main 的 push 上执行后端与前端检查，全部成功后调用同一提交的[镜像发布工作流](../.github/workflows/publish-image.yml) 自动发布，PR 和其他分支不会自动发布；同时保留独立的手动发布入口。发布通过 matrix 在 `ubuntu-24.04` 与 `ubuntu-24.04-arm` 原生 runner 分别构建，各自按 digest 推送并上传摘要；全部成功后合并、校验双架构清单，生成 latest 与完整提交 SHA 标签。构建缓存按架构隔离，发布工作流串行运行；同一分支有新提交时，会取消尚未完成的旧检查/自动发布流程。部署时等待发布与多架构合并成功，再执行 docker compose pull/up 更新运行镜像；仅推送代码或检查通过并不代表服务器会自动更新容器。

## 反向代理与限流 IP

可使用 v2way 或其他反向代理，将镜像域名的根路径 `/` 转发到 light。代理需要：

- 保留原始 Host、Authorization、Cookie、查询参数及 Registry 请求/响应头，包括 Accept、Range 和 Location。
- 覆盖设置正确的 `X-Forwarded-Proto`，不依赖 `X-Forwarded-Host`。
- 支持大请求与流式传输，设置合适的上传限额和空闲超时，不对所有请求使用短总超时或小正文限制。
- 不对 Registry、认证与管理 API 做共享缓存。

不配置外部 URL 或域名白名单。页面复制地址使用当前浏览器的 host/origin；认证 realm 根据请求 Host 和协议生成，上传 Location 使用相对路径。应用仍校验登录同源关系和管理写操作的 CSRF。

需要按代理后的真实客户端 IP 限流时，在控制台“系统设置 → 应用配置”填写“限流客户端 IP Header”（如 `X-Light-Client-IP`），保存后即时生效。留空时使用 TCP 连接来源 IP。

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

管理员账号仅用于网页管理，不能使用管理员用户名和密码执行 `docker login`；Registry 认证只接受机器人凭据。

首次成功推送自动建立仓库。机器人统一具有所有仓库的 pull/push 能力，不配置逐令牌权限范围；不能登录网页、调用管理 API 或直接删除已存储内容。

新仓库默认私有。管理员可在详情页直接切换公开状态、编辑备注；备注失焦后自动保存。仓库名称是推拉路径，创建后不可修改。

公开仓库支持匿名网页浏览和 `docker pull`，匿名用户不能推送。转回私有后，后续匿名请求和此前签发的匿名令牌均被拒绝；已经开始的传输及已经下载的内容无法追回。

首页“镜像文件占用”统计已提交 blob 的逻辑字节数，包含自有镜像内容与代理缓存 blob，按各独立存储空间内的摘要去重。排除管理 JSON、缓存清单快照、索引/标签链接及未完成上传；已完成但尚未关联标签的 blob 仍占用空间并计入。代理关闭后保留的 blob 也会统计，首次仅有空缓存元数据时为 0 B。磁盘可用空间仍取文件系统实际容量，包含这些元数据及其他文件的影响。

镜像列表与详情显示平台、创建时间和逻辑大小：

- 逻辑大小包含根清单、递归子清单、配置与压缩 layer，同一引用图内按 digest 去重。
- 多架构优先读取索引自身的 `org.opencontainers.image.created` 注解；缺失或无效时取各平台配置中的最新有效创建时间。单架构取 config.created，时间缺失时显示“—”。
- 单标签删除只解绑所选 tag，保留其它标签、索引/subject 和跨仓库仍引用的内容；执行前重新核对删除预览。

## Docker Hub 私有代理缓存

默认关闭。在控制台“系统设置 → 应用配置”打开 Docker Hub 私有代理，保存后即时生效。此功能需要 light 服务端能够通过 HTTPS 访问 Docker Hub 及其受信 CDN。

```bash
docker login registry.example.com --username robot-实际ID
docker pull registry.example.com/alpine:latest
docker pull registry.example.com/library/alpine:latest
```

自有仓库的精确 tag/manifest 优先；只有缺少仓库或引用时回源。单段名称 `alpine` 映射为 Hub 的 `library/alpine`。代理只访问 Hub 的公开镜像，必须使用 light 机器人认证；自有仓库即使公开，缺失 tag 的回源仍拒绝匿名请求。代理数据不进入公开列表，也不被仓库公开开关开放。

每个代理 manifest/index GET 或 HEAD 都重新获取、校验上游清单；Hub 不存在、限流或故障时明确报错，不返回旧索引。多架构原样保留，由客户端选择平台；同名 tag 后续主动推送至 light 后使用自有内容。删除自有 tag 后再次私有拉取可能重新回源。

blob 完整下载并校验后才响应，提交成功起固定缓存 168 小时，读取、HEAD 和清单更新不续期。冷 Range 先下载完整 blob 再返回指定范围；冷 HEAD 不下载正文。首次拉取大层时首字节等待较长，上层反代的响应等待时间应覆盖允许的回源时限。

缓存保存于独立 `hub-cache/`，不与自有 `registry/` 共享文件；每 5 分钟检查过期对象，新请求也会先回收已过期对象。清理等待在途缓存请求结束，默认不提前淘汰未满 7 天的内容，空间不足返回错误。控制台“存储维护”显示来源、上游名称、最后回源时间、已缓存 blob 字节和最早到期时间，并支持异步清理过期缓存、确认清空全部代理缓存。不为填齐页面大小而下载其它架构。

| 页面设置            | 默认值  | 用途                                                                |
| ------------------- | ------- | ------------------------------------------------------------------- |
| Docker Hub 私有代理 | 关闭    | 启用缺失引用回源                                                    |
| 缓存容量            | 10 GiB  | blob 与在途填充预算                                                 |
| 单个 blob 上限      | 20 GiB  | 最大 20 GiB，同时受总预算限制                                       |
| 回源并发数          | 4       | 清单请求、冷 HEAD 与 blob 填充共用，1–32                            |
| 回源超时            | 1800 秒 | 单次请求/填充总时限，1–3600 整数秒；清单另限 30s，网络读取空闲限 5m |

代理清单按机器人和来源 IP 分别限制 60 次/分钟，沿用可信 IP Header 规则；总请求并发 64，同摘要填充合并。元数据使用独立的 8 MiB 原子快照，最多 1024 份清单快照和 16384 个 blob 记录，并为 blob 记录预留容量；达到上限时需清理缓存。每份清单最多 4 MiB，整体仍受快照预算限制。

出站连接仅允许固定 Hub/认证和 CDN 主机，拒绝私有地址、任意跳转及含外部层 URL 的清单，不转发 light 凭据。不使用宿主 HTTP_PROXY/HTTPS_PROXY；需要服务器本身具备到上游的直连能力。Docker Hub 的上游拉取配额仍然适用；首版不支持 Hub 私有仓库、外部 registry 或 daemon registry-mirrors 自动认证。

缓存元数据损坏或持久化失败时暂停代理，自有 Registry 继续运行。修复方式为正常停机后移走整个 `hub-cache/` 目录，再启动重建缓存；勿仅删除元数据或改动内部 Distribution 文件。实际 Docker Hub 拉取和容器部署仍需单独验收，自动测试采用受控上游替身。

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

“系统设置 → 应用配置”统一管理限流 IP Header 与 Docker Hub 的开关、容量、单 blob 上限、并发和超时。首次没有已保存设置时直接采用代码默认值，无需在 Compose 填写，也不导入旧环境变量。配置保存在 `management.json`，仅管理员会话可读写；保存需要同源与 CSRF 校验，并通过设置版本防止并发覆盖。

保存后即时生效，无需重启应用。限流 IP Header 在后续请求中使用新值；关闭代理会拒绝新的代理请求，已经开始的传输继续完成。再次启用复用原缓存。回源并发使用统一计数，降低上限后等待已有任务自然降至新上限以下再接纳新任务；超时作用于新请求。

缩小缓存容量或单 blob 上限不会提前删除未满 7 天的对象，也不取消已预留空间的填充；新填充按新预算检查，不足时明确报错。配置先持久化，再发布到运行实例；持久化失败保留原运行配置，不在线重建 Registry App。

旧 `LIGHT_RATE_LIMIT_IP_HEADER`、`LIGHT_HUB_*` 环境变量不再读取，旧 `-rate-limit-ip-header` 启动参数已移除。Compose 仅保留 `LIGHT_IMAGE` 用于选择运行镜像、`TZ` 用于容器时区，这两项由部署层控制。

以下参数仍属于进程启动或离线维护配置：

| 参数                     | 默认值           | 用途                                    |
| ------------------------ | ---------------- | --------------------------------------- |
| `-data`                  | `/var/lib/light` | 数据目录                                |
| `-reserve-bytes`         | `1073741824`     | 可用空间低于 1 GiB 时拒绝新上传         |
| `-max-upload-bytes`      | `21474836480`    | 单次上传请求正文上限 20 GiB，0 表示不限 |
| `-transfer-idle-timeout` | `5m`             | 上传/下载空闲超时，不是总传输时限       |

监听端口固定为 `5080`，不提供 `-address`、`-external-url`。公开网页 API 按来源 IP/路径限制为每分钟 60 次、全局 4 并发和 15 秒计算预算；这些限制不等同于 Registry 大文件传输限制。

`/healthz` 用于存活检查，`/readyz` 用于就绪检查。`light health` 默认访问本机 `/readyz`，不获取实例锁；`light check` 是离线检查，不能代替运行时健康检查。

备份时正常停止实例，完整复制数据目录，包括 `management.json`、`registry/`、`jobs/` 和审计记录；`hub-cache/` 可一并备份或在停机时整体舍弃。`management.json` 含账户/机器人摘要及签名秘密，应按敏感数据保护。恢复时使用同版本程序检查数据，启动后重新登录、重建索引并按已知 digest 验证拉取；索引重建不能修复管理数据或镜像内容损坏。
