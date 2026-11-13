# light

[![检查](https://github.com/a1zaza/light/actions/workflows/check.yml/badge.svg)](https://github.com/a1zaza/light/actions/workflows/check.yml)

一个适合个人部署的 Docker / OCI 镜像仓库。Go 单程序内嵌中文 Web 控制台，镜像与管理数据保存在本地文件，无需数据库或 Redis。

## 功能

- **镜像托管**：支持多级仓库路径、多标签、Docker Schema 2 / OCI 清单和多架构镜像。
- **公开与私有**：默认私有，可按仓库开放匿名浏览与拉取。
- **机器人令牌**：用于 Docker、CI 和部署脚本，支持到期、停用与撤销。
- **Docker Hub 代理**：本地引用缺失时按需回源；清单每次检查上游，blob 固定缓存 7 天，代理内容始终私有。
- **中文控制台**：管理仓库备注、可见性、标签与原始清单，复制推拉命令。
- **存储维护**：单标签删除、空间回收、废弃上传清理、索引重建与操作审计。
- **页面配置**：首次使用默认值，应用设置保存后即时生效。

## 快速开始

需要 Docker 和 Docker Compose。项目面向 **Linux 本地文件系统、单实例部署**。

```bash
git clone https://github.com/a1zaza/light.git
cd light
mkdir -p data
chmod 700 data
```

数据目录需允许容器用户 `1000:1000` 读写。

### 使用发布镜像

Compose 默认使用 `ghcr.io/a1zaza/light:latest`。若镜像尚未发布或不可拉取，使用下方的本地构建方式。

```bash
docker compose pull
docker compose up -d
```

### 从源码构建

```bash
docker build -f deploy/Dockerfile -t light:local .
export LIGHT_IMAGE=light:local
docker compose up -d
```

首次启动自动创建管理员 `admin`，并在启动日志中输出随机初始密码，无需单独执行初始化命令。使用以下命令查看：

```bash
docker compose logs light
```

初始凭据只在创建账号时输出一次，重新启动不会重置或再次输出密码。登录后可在“系统设置”修改账号和密码。`LIGHT_IMAGE` 应在后续 Compose 命令所在的终端保持一致。

启动后访问 **[http://127.0.0.1:5080](http://127.0.0.1:5080)**。公网使用时，为镜像域名配置 HTTPS 反向代理，见[部署与运维](docs/operations.md)。

## 推送与拉取

登录控制台，在“机器人令牌”中创建凭据，再使用 Docker 登录。机器人秘密仅创建时显示一次。

```bash
docker login registry.example.com --username robot-实际ID
docker tag app:latest registry.example.com/personal/app:latest
docker push registry.example.com/personal/app:latest
docker pull registry.example.com/personal/app:latest
```

将域名、用户名和镜像名替换为实际值。首次推送自动建立私有仓库，仓库名称决定推拉路径，创建后不可修改。机器人统一推拉全部仓库；网页管理使用管理员账户。

在仓库详情页开启“公开”后，其他人即可匿名浏览和拉取。

## Docker Hub 代理缓存

在“系统设置 → 应用配置”启用 Docker Hub 私有代理，保存即时生效，无需修改 Compose 或重启应用。

```bash
docker login registry.example.com --username robot-实际ID
docker pull registry.example.com/alpine:latest
```

- 自有镜像优先；本地仓库、tag 或摘要缺失时回源。`alpine` 映射为 Hub 的 `library/alpine`。
- 每次拉取清单都重新检查上游；上游错误时明确失败，不返回旧索引。
- blob 完整下载并校验后才返回，缓存 7 天，读取不续期。
- 即使同名自有仓库公开，代理内容仍须机器人认证；缓存可在“存储维护”查看和清理。

默认缓存预算为 **10 GiB**、回源并发 **4**、总超时 **30 分钟**，可在页面调整。服务端需能直连 Docker Hub 及其 CDN；仅支持 Hub 公开镜像，上游拉取限额仍然适用。首次拉取大层可能需要等待完整下载，详见[代理行为与限制](docs/operations.md#docker-hub-私有代理缓存)。

## 开发

后端使用 Go 1.26.11，前端使用 Node.js 24 / npm 11；Registry 基于 CNCF Distribution v3.1.2，控制台使用 React / TypeScript。架构和界面参考 v2way。

```bash
go run ./cmd/light serve -data .data
```

首次启动时从后端终端输出获取管理员初始凭据。另开终端启动前端：

```bash
cd web
npm ci --ignore-scripts
npm run dev -- --port 5174 --strictPort
```

访问 `http://127.0.0.1:5174`，Vite 会代理后端接口。开发数据 `.data` 与 Compose 数据 `data` 分开使用。

```bash
go test -race ./...
go vet ./...
cd web
npm run typecheck
npm test
npm run format:check
```

## 文档

- [部署、反向代理、配置与备份](docs/operations.md)
- [实施状态与验证范围](docs/implementation-status.md)
- [Docker Hub 代理缓存设计](docs/docker-hub-proxy-cache-design.md)
- [变更记录](docs/changelogs/2026-11.md)
