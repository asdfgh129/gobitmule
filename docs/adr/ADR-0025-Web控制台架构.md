# ADR-0025 Web 控制台架构（WBS 20）

- **状态**：Accepted（项目发起人 2026-11-20 指令「针对已开发功能设计 Web UI 与 RPC 接口，采用 Vue3 + TS」；代行架构/产品角色，非实名会签，依 `17` §1 P0 先例）
- **日期**：2026-11-20
- **决策者**：项目发起人（用户）
- **关联**：ADR-0016（Daemon 加可替换 UI）、ADR-0019（不实现 aria2 RPC 兼容层）、`03` FR-UI、`05` §11、`07` v0.14 §9、`13` v0.32、`14` WBS 20、`23` v1.5

## 背景

`internal/api/ui/index.html` 自 P0 起是单文件占位页（147 行内联 HTML/CSS/JS），只能
列任务、按状态按钮与轮询刷新，无法承载 P2 已落地的能力：HLS/DASH 流任务、
站点解析预览、remux/ffmpeg 后处理、能力矩阵、凭据引用、事件时间线与离线运维入口。
与此同时，daemon 是零 cgo 单二进制，控制面是 17 条 REST 路由 + 一条 push-only
WebSocket；新增前端资源路由或引入 JSON-RPC 传输都会扩大契约面。

## 决策

1. **前端技术栈固定为 Vue 3 + TypeScript + Vite + Pinia + vue-router**，
   严格类型检查（`vue-tsc --noEmit`），不引入重型组件库 / 图表库 / 外部 CDN 字体，
   保持离线可用。图标为内置 stroke SVG 集合。
2. **仍以 L0 REST/WS 为唯一线协议**。所谓「RPC 接口」是 `web/src/api/rpc.ts`
   的命名方法层（`system.*` / `tasks.*` / `config.*` / `credentials.*` / `events.*`），
   与 `web/src/types/api.ts` 共享类型、统一错误对象。**不新增 JSON-RPC 端点**，
   ADR-0019 的边界不变；CLI 与第三方客户端契约零改动。
3. **单文件嵌入**：Vite + `vite-plugin-singlefile` 把 JS/CSS 全部内联到
   `internal/api/ui/index.html`，`go:embed ui/index.html` 与 `serveUI` 不改；
   hash 路由适配「服务端只提供 `/`」。产物与 `docs/_site/` 一样版本化入库，
   `make web` 重建、`make web-check` 校验一致性，CI 重建失败即红灯。
4. **页面按控制面对象划分五个分区**：概览（健康 / 吞吐 / 能力矩阵 / 实时事件）、
   任务（筛选 / 详情抽屉 / 生命周期操作）、新建下载（只读解析预览 + 批量）、
   设置（6 个可热更新键 + 只读启动项 + 凭据 / 连接）、运维（健康检查 / 事件历史 /
   离线 CLI 命令）。表单只暴露已实现能力。
5. **鉴权与实时性**：本机回环免鉴权；远程模式由 `POST /ws-ticket` 用 Bearer 换取
   60 秒一次性票据，`WS /events?ticket=` 升级（长期令牌不进 URL）。
   `task.progress` 只含字节数，速度 / ETA 通过 `GET /tasks/{id}` 轮询补足；
   首屏事件用新增 `GET /events/history` 回填。
6. **诚实边界优先于界面完整度**：`udctl backup / restore / migrate` 是离线 CLI
   （restore / migrate 明确拒绝 daemon 运行时执行），页面仅展示准确命令与原因；
   分片明细、`capability.changed` 发布、sidecar 运行时开关、分离 A/V 合并、
   真实站点回放均未实现，不做成可点击的假按钮。「重试」映射为既有 `resume`。

## 落地

- 源码 `web/`（Vue 3 + TS + Vite + Pinia + vue-router，`package-lock.json` 提交）；
  产物 `internal/api/ui/index.html`（约 174 KiB / gzip 63 KiB）。
- Go 新增两条 additive 端点：`GET /api/v1/events/history`、`POST /api/v1/ws-ticket`
  （`internal/api/server.go` + `ui_rpc_test.go`）；`daemon.go` 注入 `EventHistory: st.Events`。
- 构建链：`Makefile` 的 `web` / `web-check` 目标；CI `setup-node`（Node 22）→
  `make web && make web-check` → 既有 Go 链。
- 接口映射真值源 `docs/07` v0.14 §9；构建真值源 `docs/13` v0.32。

## 后果

- 正面：控制面从「验证契约的占位页」升级为可日常使用的控制台；前端改动不触碰
  Go 路由与二进制接口；类型化 RPC 层让接口漂移在编译期暴露；远程 WebSocket 有
  可用的浏览器鉴权路径而不泄露长期令牌。
- 代价：新增 Node 22 构建前置条件与 ~174 KiB 嵌入式 HTML；单文件产物使源码 diff
  不可读，必须以 `web/` 源码为准并靠 `make web-check` 防陈旧；事件历史无分页
  （固定上限 200）。
- 后续：若出现多文件资源选择 / 分离的独立前端部署需求，再评估 `web/dist` 目录嵌入
  与 SPA 路由守卫；届时须同步升级 `serveUI` 契约测试与 CI 校验，本决策不预先扩张。
