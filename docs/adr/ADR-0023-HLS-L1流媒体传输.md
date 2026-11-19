# ADR-0023 HLS L1 流媒体传输（WBS 13 半边落地与范围裁决）

- **状态**：Accepted（项目发起人 2026-11-19 全权授权按建议推进开发；代行五角色，非实名会签，依 `17` §1 P0 先例）
- **日期**：2026-11-19
- **决策者**：项目发起人（用户）于 2026-11-19 指令「按下一步开发计划推进剩余未开发功能」，并授权按执行方建议裁决
- **关联**：`03` FR-TRN-08、`04` §架构图 / 零 sidecar 基线、`05` §3 `ProtocolIdentity` / §10 Resolver 链 / §9 PostProcessor、`06` §3 数据模型、`08`、`10` §4、`12` §3、`14` WBS 13、`16` §互操作矩阵、`09`（零 sidecar 不变）

## 背景

P2 的 WBS 13「流媒体」要求 HLS/DASH 清单解析、AES-128 解密、按分辨率选流、
分片并发与顺序提交（FR-TRN-08，M 级）。核心模型在 P0 一次建模时已预留
`ProtocolIdentity.VariantID / IsAdaptive` 与 `ResourceKind "stream"`（ADR-0013、
`06` §3），传输层一直是零 sidecar 基线（`04` §传输：HLS 全为 Go 库）。

落地时需要裁决四件事：范围（HLS 与 DASH 的先后）、live 流与加密算法的取舍、
变体选择策略、断点续传的字节映射。

## 决策

1. **范围：本次只落地 HLS，DASH 留待同一 WBS 的后续包。**
   HLS 是 FR-TRN-08 的主体（RFC 8216），解析器与传输层纯 Go、零依赖、可在本机
   用 httptest 全量测试；DASH（MPD / SegmentTemplate / 多 Representation）的
   工作量与 HLS 相当且测试 fixture 更重，独立成包推进，避免一次合并引入
   半成品。WBS 13 状态记为「HLS 已交付、DASH 待做」，不虚报。

2. **仅 VOD：媒体清单缺 `EXT-X-ENDLIST` 即拒绝。**
   live 流永不结束，与「分片总数固定 + 崩溃恢复复用分片」的存储模型冲突；
   强行支持会产生无限任务。拒绝时给 `E_CAPABILITY_UNAVAILABLE`，如实报
   `live not supported`，不静默截断。

3. **仅 `METHOD=AES-128`（CBC + PKCS7），`SAMPLE-AES` 拒绝。**
   AES-128 覆盖公网绝大多数加密 HLS；SAMPLE-AES 涉及 fMP4 CENC，与 Remux
   （WBS 14 纯 Go Remux）耦合，留到 Remux 落地时一并裁决。IV 按规范：显式
   `IV` 属性优先，否则用 128 位大端媒体序列号。

4. **变体选择：`BANDWIDTH` 最高者，并列取文档序；无 `BANDWIDTH` 的条目垫底。**
   与 yt-dlp「最高画质」默认一致；`{"hls": true}` 任务选项可强制分类无扩展名
   的 token 化清单 URL。选流发生在传输层 Probe/Run（resolver 只做分类，不在
   解析阶段重复拉流），`IsAdaptive` 在分类时置 true。

5. **断点续传按字节边界映射：首个 pending 分片的字节偏移定位到段，段内用
   HTTP Range 跳过。**
   分片真值仍是 `chunks` 表（崩溃恢复语义不变）。已验证字节随流重过网络并
   计入 Budget（限速权威唯一），但不落盘；段级跳过把浪费限制在单段内。
   逐段尺寸在 Probe/Run 各做一次有界并发 `Range: bytes=0-0` 探测（部分 CDN
   拒 HEAD），代价是 2N 个 1 字节请求，换取总尺寸精确与续传映射。

6. **内存与并发：按探测段长计的 64MiB 字节信号量 + 8 worker 上限 + 单段
   512MiB 硬帽。**
   「分片并发、顺序提交」由有界 worker 池 + 按序整理 goroutine 实现：乱序
   下载、按播放列表顺序提交进 ChunkWriter。段体按序缓冲受字节信号量约束，
   CDN 发超大段不会撑爆进程（移动端约束，`04` 零 sidecar 基线）。

7. **输出容器：TS 段拼接为 `.ts`，fMP4 段（`EXT-X-MAP` / `.m4s`）拼接为
   `.mp4`。**
   拼接流可直接被播放器读取；规范 MP4（moov / faststart / PTS/DTS 重基准）
   由 WBS 14 纯 Go Remux 负责（`05` §9），本传输不做任何封装变换。

## 落地

| 文件 | 内容 |
|---|---|
| `internal/transport/hls/playlist.go` | RFC 8216 master / media 解析、属性表、BYTERANGE、fMP4 探测、未知 tag 容忍 |
| `internal/transport/hls/crypto.go` | AES-128 CBC 解密、IV 解析（显式 / 序列号）、PKCS7 校验 |
| `internal/transport/hls/hls.go` | Transport：有界探测、字节信号量 worker 池、按序提交、Budget 唯一计费、key 缓存 |
| `internal/transport/hls/errors.go` | 稳定哨兵错误（调度器按 `E_CAPABILITY_UNAVAILABLE` / `E_RESOLVE_FAILED` 分类） |
| `internal/resolve/hls.go` | `.m3u8` 分类 + `{"hls": true}` 强制分类，http(s) 白名单，SSRF 出口校验 |
| `internal/app/daemon.go` | 链序：resolver `…torrent → hls → [plugin] → direct-http…`；transport 追加注册 |
| `tools/gates.py` | 新包覆盖率阈值 80% |

边界（不宣称）：live / SAMPLE-AES / DASH / 多音轨字幕轨 / 广告插入（
`EXT-X-DISCONTINUITY` 仅容忍不处理）/ 站点页解析（WBS 12 yt-dlp）均不支持或
未落地；真实公网站点的互通未验证，本包证据为本机 httptest 与单测。

## 后果

- FR-TRN-08 的 HLS 半边达成；`12` §3 的对应用例随本包补齐。DASH 与 Remux
  仍是 WBS 13/14 的未完项。
- 传输注册表多一个实现，`/capabilities` 的 transports 列表新增 `hls`；
  check_arch 守卫不变（热路径零 exec / 零 cgo）。
- Resolver 链在 torrent 之后插入 hls：协议原生分类优先于插件不变；插件
  规则无法吞掉 `.m3u8` 直链（`05` §10）。
