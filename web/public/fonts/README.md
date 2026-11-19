# 字体资源

界面字体顺序沿用参考项目：`Consolas, 'LXGW WenKai', monospace`。

- `lxgwzk.woff2`：从 `../v2way/web/src/assets/fonts/lxgwzk.woff2` 原样复用，字体内部名称为 **LXGW ZhenKai / 霞鹜臻楷**。参考项目使用 `LXGW WenKai` 作为 CSS 名称，本项目保留该名称以匹配字体配置。
- 上游：[lxgw/LxgwZhenKai](https://github.com/lxgw/LxgwZhenKai)。许可证为 SIL Open Font License 1.1，完整原文随附于 [OFL-LXGW-ZhenKai.txt](OFL-LXGW-ZhenKai.txt)，来源为[上游许可证](https://github.com/lxgw/LxgwZhenKai/blob/main/OFL.txt)。
- 文件 SHA-256：`19609aae5bb873a2b27b87cbe082576153b37ec46d5187bb7ad6ae3f74561c39`。文件未重新压缩、裁剪或修改内部名称，仅用于网页字体交付。
- `consolas.woff`：按用户要求从 `../v2way/web/src/assets/fonts/consolas.woff` 原样复制，内部名称为 **Consolas / Regular**；SHA-256：`a512f7a65dffb8330b13dd53df4ab30ec73416be99ce789f55e19f1d82cd6aca`。通过项目自身的 `/fonts/consolas.woff` 加载，不依赖设备安装字体。Consolas 为 Microsoft 字体，版权和授权独立于霞鹜臻楷的 SIL OFL；相关说明见 [Microsoft 字体 FAQ](https://learn.microsoft.com/en-us/typography/fonts/font-faq)，本目录的 OFL 文件仅对应霞鹜臻楷。

`/fonts.css` 由控制台和无前端产物提示页共用，`font-display: swap` 避免字体加载阻塞文字显示。字体通过应用自身静态资源提供，无外部字体 CDN 依赖。
