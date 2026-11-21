# ADR-0028 HLS 多 rendition 分离发射（FilePlanner 能力）

- **状态**：Accepted（项目发起人 2026-11-21 全权授权按建议顺序执行 P3；代行五角色，非实名会签，依 `17` §1 P0 先例）
- **日期**：2026-11-21
- **决策者**：项目发起人（用户）指令「按建议顺序执行」
- **关联**：ADR-0027（多文件下载与合并）、ADR-0023（HLS L1）、ADR-0010（验证清单）、`03` FR-TRN-08 / UC-04、`05` §3、`06` §4.4、`07` v0.14、`12` §3.3、`14` WBS 13

## 背景

ADR-0027 打通了调度器多文件下载与 Remux 合并，但两个流传输当时只接受单流资源：
HLS 拒绝多 rendition（EXT-X-MEDIA 音频组被忽略），DASH 拒绝多 AdaptationSet。
「HLS 多 rendition / DASH 多 AdaptationSet 分离发射」是 P3 队列次项。

## 决策

1. **可选 `core.FilePlanner` 能力接口**（`PlanFiles(ctx, r) ([]ResourceFile, error)`）：
   流传输可声明「本资源可展开为多个每流文件」的计划；调度器在下载阶段前采纳计划
   （`ReplaceResourceFiles` 原子替换 files 表行，镜像与身份不动），随后按 ADR-0027
   的多文件路径逐文件下载。无该能力或计划 ≤1 文件 → 单文件路径不变。接口为
   **可选能力**（类型断言），不破坏既有 Transport 实现与测试替身。
2. **HLS 的计划来源是主清单**：EXT-X-MEDIA `TYPE=AUDIO` 组按 `GROUP-ID` 归组，
   `EXT-X-STREAM-INF` 的 `AUDIO` 属性引用；选流沿用带宽策略，音频 rendition 按
   `DEFAULT=YES > AUTOSELECT=YES > 文档序` 选取；**URI 为空的 rendition（muxed
   音频）永不拆分**——空 URI 不能相对解析为自 URL（实现缺陷实测修复）。计划两文件：
   文件 0 = 视频 media playlist URL（`Path` 同时充当合并后最终名 `<base>.mp4`），
   文件 1 = 音频 media playlist URL。媒体清单（非主清单）与无音频组的主清单不拆分。
3. **未知尺寸由调度器逐文件 Probe 补齐**：多文件初始化对 `Size<=0` 的文件构造
   子 Resource 调同一 Transport 的 Probe（HLS = 段尺寸探测，HTTP = Range 探测），
   探不出正尺寸干净拒绝。尺寸声明优先，探测兜底。
4. **子资源头拷贝**：多文件循环的子 Resource 携带 `res.Headers`（内部分发标记
   `X-Ud-Stream` 与站点头），保证逐文件传输分派正确（HLS 子文件仍选中 hls）。
5. **DASH 多 AdaptationSet 维持拒绝**：DASH 的每文件需要表示级选择（MPD URL
   相同、集合靠解析区分），子文件语义与 HLS 的「每文件一个播放清单 URL」不同构；
   边界如实保留，指向后续包。

## 发现并修复

- **变量遮蔽产生零长度 chunk**：多文件初始化分支内 `sizes := make(map...)` 遮蔽
  外层尺寸切片，`CreateChunks(0)` 产出零长分片、文件全零、校验却被空 digest
  短路通过——集成测试抓出后修复并留注释。
- **空 URI 相对解析**：URI 为空的 EXT-X-MEDIA 行被 `resolveURI` 解析成自 URL，
  muxed 音频被误拆——解析前置空检查。
- **`.transcode.part` 类问题同源**：文件 0 的 `Path` 是合并后最终名（`<base>.mp4`），
  避免「最终名等于播放清单名」的扩展名混淆。

## 落地

| 文件 | 内容 |
|---|---|
| `internal/core/interfaces.go` | `FilePlanner` 可选接口 |
| `internal/transport/hls/playlist.go` | EXT-X-MEDIA 解析、AudioGroups、`SelectAudioRendition`、空 URI 保护 |
| `internal/transport/hls/hls.go` | `PlanFiles`（主清单 → 双文件计划） |
| `internal/scheduler/scheduler.go` | 计划采纳（`ReplaceResourceFiles`）、逐文件 Probe、子资源头拷贝 |
| `internal/store/resources.go` | `ReplaceResourceFiles` |
| `internal/scheduler/hls_multi_test.go` | 调度器级集成：主清单+音频组 → 双播放清单下载 → remux 合并 |

## 验证

- 解析/计划单测：EXT-X-MEDIA 归组、DEFAULT/AUTOSELECT 选取、muxed 不拆分、
  媒体清单不拆分、缺 GROUP-ID 拒绝、PlanFiles 双文件与不拆分路径。
- **调度器级集成**（`TestHLSSeparatedAudioRenditionRemuxToMP4`，真实 hls 传输 +
  真实 Remux，零 ffmpeg）：主清单+音频组 → 双媒体播放清单逐文件下载（PlanFiles →
  Probe 补尺寸 → chunk 计划）→ remux 合并单 MP4，双轨样例字节断言。
- 单文件回归：既有 HLS 测试（loopback 下载、变体选择、加密、续传）全绿。

**边界（不宣称）**：DASH 多 AdaptationSet、HLS 多视频流（iframe / 同组多视频）、
字幕轨下载与烧录、真实站点端到端仍未实现；合并上限沿用 Remux 1GiB 输入帽。

## 后果

- FR-TRN-08 的 HLS 分离流路径打通；ADR-0010 验证清单的 HLS 侧同批兑现。
- `FilePlanner` 成为流传输的可选扩展点：DASH 多集与后续字幕/多轨能力按同一
  接口接入，调度器零改动。
