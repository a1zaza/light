# 字体资源

界面字体顺序沿用参考项目：`Consolas, 'LXGW WenKai', monospace`。

- `lxgwzk.woff2`：从 `../v2way/web/src/assets/fonts/lxgwzk.woff2` 原样复用，字体内部名称为 **LXGW ZhenKai / 霞鹜臻楷**。参考项目使用 `LXGW WenKai` 作为 CSS 名称，本项目保留该名称以匹配字体配置。
- 上游：[lxgw/LxgwZhenKai](https://github.com/lxgw/LxgwZhenKai)。许可证为 SIL Open Font License 1.1，完整原文随附于 [OFL-LXGW-ZhenKai.txt](OFL-LXGW-ZhenKai.txt)，来源为[上游许可证](https://github.com/lxgw/LxgwZhenKai/blob/main/OFL.txt)。
- 文件 SHA-256：`19609aae5bb873a2b27b87cbe082576153b37ec46d5187bb7ad6ae3f74561c39`。文件未重新压缩、裁剪或修改内部名称，仅用于网页字体交付。
- Consolas 通过 `local()` 使用用户设备已安装的字体，不随项目分发；没有复制参考项目中的 `consolas.woff`。相关授权说明见 [Microsoft 字体 FAQ](https://learn.microsoft.com/en-us/typography/fonts/font-faq)。未安装时按字体栈回退。

`/fonts.css` 由控制台和无前端产物提示页共用，`font-display: swap` 避免字体加载阻塞文字显示。字体通过应用自身静态资源提供，无外部字体 CDN 依赖。
