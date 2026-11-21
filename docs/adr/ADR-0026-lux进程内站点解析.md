# ADR-0026 lux 进程内站点解析（精选提取器子集）

- **状态**：Accepted（项目发起人 2026-11-20/21 全权授权按建议顺序推进开发；代行五角色，非实名会签，依 `17` §1 P0 先例）
- **日期**：2026-11-21
- **决策者**：项目发起人（用户）指令「对未开发的任务进行开发」，授权按执行方建议裁决
- **关联**：ADR-0010（视频站双层解析）、ADR-0023/0024（流传输与分发）、`03` FR-SRC-01、`05` §10、`10` §4 依赖清单、`12` §1（CI 禁直连真实站点）、`14` WBS 12

## 背景

ADR-0010 决策 1 的进程内半边（`lux` + goja 规则插件）在 2026-11-20 的 WBS 12
交付中仍是未注册占位（`docs/14` ⚠️ 标注「site.go 的 lux 进程内解析仍未接入链」）。
同批已落地 yt-dlp sidecar（默认关）与 WBS 14 内置 Remux。本次收口 lux 半边。

## 勘察与裁决

**依赖足迹实测**（lux v0.24.1）：
- 导入全部 `extractors` 树 → **160+ 链接模块**（含 colly 抓取框架、第二 JS 引擎
  otto、kkdai/youtube、urfave/cli、进度条）；
- **精选提取器子集**（站点提取器按 init() 自注册，未 import 不链接）——
  bilibili / douyin / iqiyi / mgtv / ixigua / kuaishou → **18 个链接模块**，
  全部 MIT/BSD（许可证门禁实测 PASS：0 copyleft / 0 unknown）。

**裁决：按精选子集接入**。未列入子集的站点（youku / 微博 / qq 等）不链接对应
提取器，Resolve 前置域名预检直接给 `E_CAPABILITY_UNAVAILABLE`，绝不把未覆盖
站点交给 lux（其分发在无匹配时会取 `extractorMap[""]` = nil 并 panic——实测发现，
见「发现并修复」）。扩大站点覆盖 = 增加一行 import + 一个域名条目 + 许可证门禁
复跑，属低风险运维动作。

## 决策

1. **opt-in 配置**：`sources.lux.enabled`（默认 `false`）+ `timeout`（1s..5m，
   默认 60s）。关闭时解析链与既有行为逐字节一致；与 yt-dlp 的 opt-in 姿态同构。
   lux 是进程内纯 Go，不影响零 sidecar 基线。
2. **SSRF 双检**：页面 URL 进提取器前过 Guard；提取产出的媒体 URL 在 Resolver
   出口再过 Guard（docs/05 §10）。提取器内部对同站页面 / API 的抓取属
   ADR-0010 已接受的设计（页面 URL 已检，出口 URL 复检）。
3. **两处库级缺陷防御**（实测发现）：
   - lux 分发对无匹配站点取 `extractorMap[""]`（nil）并 **panic** → Resolve
     先做域名覆盖预检（子域名按后缀匹配，`notbilibili.com` 类形近域拒绝）；
   - 站点提取器解析敌意页面内容可能 panic → 提取 goroutine 统一 recover，
     归类为 `E_TRANSIENT_NETWORK`，绝不带崩 daemon。
4. **选流确定性**：最大 `Size` 优先，并列时按流 ID 字典序；流 ID 以 map key
   为权威（真实分发会经 `FillUpStreamsData` 填充，fixture 可省略）。
5. **单 Part 边界**：调度器每任务只下载 `Resource.Files[0]`，多 Part（分段
   待合并）流拒绝并指向 P3 合并管线——与 yt-dlp「仅渐进格式」边界同构，不
   生成假成功文件。
6. **站点 Referer 头**：lux 的 `Part` 结构不含 headers，而 B站等 CDN 无
   Referer 即 403——按域名的静态头表（`luxSiteHeaders`）随 Resource 下发；
   纯站点公开知识，无 cookie / 无密钥。
7. **超时放弃语义**：lux `Extract` 不支持 ctx；超时后放弃调用并返回分类
   错误，被放弃的 goroutine 由提取自身自然结束（如实记录，不伪装取消）。
8. **CI 离线纪律**：提取函数为可注入 seam，测试用假提取器 + 本地 fixture；
   真实站点互通不属本包宣称范围（`12` §1）。

## 发现并修复

- **占位清理**：`internal/resolve/site.go`（未注册占位 LuxResolver +
  未接链的 SelectBestFormat 工具集）整体移除，正式实现在 `lux.go`；
  yt-dlp 的自有评分并行实现于 `ytdlp.go`，两者不共享死代码。
- **nil 分发 panic**（上述 3a）：forced 路由测试直接触发，双层防御后
  forced 到未覆盖站点得到干净分类错误。
- **extractorMap 空 ID**：fixture 与部分提取器可能不填 `Stream.ID`，
  `pickStream` 以 map key 兜底（`VariantID` 因此恒非空）。

## 落地

| 文件 | 内容 |
|---|---|
| `internal/resolve/lux.go` | 精选子集 import、域名覆盖预检、有界提取（recover）、选流、出口 Guard、站点头 |
| `internal/config/config.go` | `sources.lux.{enabled,timeout}` + Validate |
| `internal/app/daemon.go` | opt-in 注册（site 解析器位次：lux 先于 yt-dlp，均先于插件钩子） |
| `internal/resolve/registry.go` | forced 路由加 `lux` |
| `go.mod` | `github.com/iawia002/lux v0.24.1`（MIT；+12 链接模块） |
| `config.example.yaml` | sources.lux 注释块 |

边界（不宣称）：youku / 微博 / qq 等未列入子集的站点不支持；多 Part 合并、
分离 A/V 双流下载 + Remux 合成、Deno/EJS、真实站点互通验证均未达成（P3，
ADR-0010 时点注口径不变）。

## 后果

- FR-SRC-01 的进程内半边达成（进程内提取 + 站点头 + SSRF 双检；goja 规则包
  路径由既有 WBS 10 插件引擎承担）。
- 解析链（opt-in 全开）：`metalink → link-wrapper → magnet → torrent → hls →
  dash → lux → yt-dlp → [plugin] → direct-http → direct-file`。
- +12 链接模块进入 SBOM / 许可证 / govulncheck 门禁面；三门禁实测 PASS。
