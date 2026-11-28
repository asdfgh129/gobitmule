# 26 eD2k UDP 互通验证报告

> 文档编号：UD-DOC-26 ｜ 版本：v1.0 ｜ 状态：**验证报告（本地交叉验证 + 真实服务器抓包；不作发布门禁）** ｜ 日期：2026-11-28 ｜ 维护责任：架构组
>
> 本文只讨论 eD2k 的 **server UDP 数据报链路**（全局搜索、全局查源、能力协商与混淆信封）。
> TCP 登录/源交换、SecureIdent、KAD 的结论见 [`16`](16-互操作性与兼容性设计.md) §3.1 / §6.1。

## 1. 验证目标与边界

| 目标 | 结论 | 依据类型 |
|---|---|---|
| UDP 搜索 opcode 按服务器能力选择 | **PASS** | 真实服务器抓包 + 单测 |
| `0x90` tag set、`0x92` 裸搜索树、`0x98` legacy 三种格式 | **PASS** | 真实服务器抓包（`0x90`）+ 单测（`0x92`/`0x98`） |
| UDP 查源 `0x94` / `0x9A` 与 `0x9B` 回包解析 | **PASS** | 真实服务器抓包 + 黄金样本回归测试 |
| 能力位时序（登录尚未拿到 `OP_SERVERIDENT` 时） | **PASS** | 真实服务器日志 + 单测 |
| RC4 信封与 eMule 字节级互认 | **PASS（交叉验证）** | 独立 eMule 算法实现的向量测试 + 真实 UDP socket 端到端 |
| RC4 信封在**真实下发 `ST_UDPKEY` 的服务器**上端到端 | **未执行** | 抽样 40 台公开服务器均为 `udpKey=false`，见 `dist/ed2k-interop/server-capabilities.txt` |
| `0x92` 分支在真实服务器上的往返 | **未执行** | 抽样服务器要么 `udpFlags=0x0`，要么 `EXT_GETFILES`+`LARGEFILES`（走 `0x90`） |

「PASS（交叉验证）」的含义：测试里的对端编码器是**按 eMule 源码另行实现**的，不复用我们的
`EncryptServerDatagram`/`DecryptServerDatagram`，因此共享实现错误不会让测试通过；但它仍然是
本地模拟对端，**不等价于真实第三方服务器**，不得据此对外宣称真机互操作通过。

## 2. 参考基准

以 `C:/Users/bemin/WorkBuddy/bemin/eMule/srchybrid`（eMule 官方 srchybrid）为唯一线协议基准：

| 主题 | eMule 位置 |
|---|---|
| UDP 搜索 opcode 三分支 | `SearchResultsWnd.cpp` 278–322（`CSearchResultsWnd::DoNewED2KSearch`） |
| 能力位定义 | `server.h` 45–52（`SRV_UDPFLG_*`） |
| 能力位/UDP key 从 server.met 读取 | `server.cpp` 205–230（`ST_UDPFLAGS` / `ST_UDPKEY` / `ST_UDPKEYIP`） |
| `OP_IDCHANGE` 字段 | `ServerSocket.cpp` 274–330（20 字节，第四字段 = server reported IP） |
| UDP key 绑定校验 | `server.cpp` 308–313（`CServer::GetServerKeyUDP`） |
| UDP 源解析 | `UDPSocket.cpp` 285–322 + `PartFile.cpp` 2968（`CPartFile::AddSources`） |
| RC4 信封 | `EncryptedDatagramSocket.cpp` 41–63 / 390–480；`otherfunctions.cpp` 3655–3703 |

## 3. 报文格式与实测证据

### 3.1 搜索请求：三个版本不能混用

eMule 的判定顺序是：

```
EXT_GETFILES && LARGEFILES  -> 0x90 OP_GLOBSEARCHREQ3  <tagcount=1><tag><search tree>
EXT_GETFILES                -> 0x92 OP_GLOBSEARCHREQ2  <search tree>
其余                        -> 0x98 OP_GLOBSEARCHREQ   <search tree>
```

早期实现把 `0x90` 当作「`0x92` 换一个 opcode」发裸搜索树，服务器会把搜索表达式的前 4 字节
读成 tag 数，直接丢弃整包。现在 `UDPGlobSearchDatagram()` 按上表选择，`0x90` 先写
`CT_SERVER_UDPSEARCH_FLAGS(0x0E)=0x01` 的 uint32 tag。

**真实抓包**（`dist/ed2k-interop/udp-search.pcapng`，服务器 `57.128.161.24:6666` Cyberchrist 18.1）：

```
→ e3 90 | 01000000 | 830e 01000000 | 01 0600 64656269616e
         tag 数=1   tag 0x0E=1      搜索树 "debian"
← e3 99 | 3d54adcd…0d42 | fb8d0600 | 841a | 05000000 | <tags…>
```

`0x90` 请求发出后收到 10 个 `0x99` 结果包，日志逐个 `server udp search results got=1 added=1`。

`0x92` 的格式差异只有 tag set：包体直接是 `01 0600 "debian"`，没有 `01 00 00 00` 前缀；
由 `TestUDPSearchReqV2WireFormat` 按字节断言（`0x92` 的 `wire[2]==0x01` 是搜索项类型，**不是**
tag 数，二者要靠 `wire[3:6]` 区分）。

### 3.2 查源：`0x94` 与 `0x9B`

```
→ e3 94 | <hash 16> | <filesize 4>       EXT_GETSOURCES2
→ e3 9a | <hash 16>                      legacy（无 EXT_GETSOURCES2）
← e3 9b | <hash 16> | <count 1> | <ip 4><port 2> × count
```

**真实抓包**（`udp-cyberchrist.pcapng`）：

```
→ e394 d6da273629b1c6781e96e0bab4142602 97843e14     (size=339641495)
← e39b d6da273629b1c6781e96e0bab4142602 02
       041a0100 3612   -> 4.26.1.0:4662
       ffc00500 5a88   -> 255.192.5.0:34906
```

两条「源」的首字节编码 < `0x01000000`，即 eMule `IsLowID` 判据下的 LowID；该垃圾文件本就
没有可达源，客户端按同一判据过滤，日志 `server udp sources got=2 added=0`。这段真实回包已固化为
`TestParseUDPFoundSourcesRealServerAnswer` 黄金样本，防止解析器的字节序/帧长回归。

### 3.3 能力位时序：为什么需要 server.met 缓存

搜索框可能在 `OP_IDCHANGE` 之后几十毫秒就发 UDP 请求，而 `OP_SERVERIDENT`（携带 `ST_UDPFLAGS`、
`ST_UDPKEY`）要再等约 300 ms。首版实测出现：

```
23:04:20.160  server udp -> glob search "debian" ... opcode=0x98 flags=0x0
23:04:20.241  server <- ServerIdent ... udpFlags=0x0000073b
```

即能力位已知之前就按 legacy 发出。eMule 的做法是把 `ST_UDPFLAGS` 写回 server.met
（`server.cpp` case ST_UDPFLAGS），启动时就能读到。现已对齐：

1. `ServerMetEntry.UDPFlags()` 读取 `ST_UDPFLAGS(0x92)`；`LoadServerMet` 时
   `Session.SeedServerCapabilities()` 预热，key 同时用显示地址与数字端点。
2. `OP_SERVERIDENT` 到达后 `RememberServerCapabilities()` 回填缓存。
3. `ServerConnection.HasServerIdent()` 区分「尚未下发」与「确实不支持」，
   已解析过 ident 的服务器以实况为准，避免用过期缓存覆盖 `udpFlags=0`。

修复后探针先等 `OP_SERVERIDENT` 再搜索，实测变为 `opcode=0x90 flags=0x73b`。

> **注意**：本地 `server.met` 给 Cyberchrist 记录的是 `0x17fb`，而实时 `OP_SERVERIDENT` 报
> `0x73b`。两者不一致是网络数据的常态（服务器升级、列表陈旧），必须用实况优先；`0x17fb`
> 是当前会话在 ident 到达前曾用于选 `0x94` 的值，不影响端到端结果。

## 4. RC4 服务器信封

### 4.1 算法（逐项对齐 eMule）

```
key     = MD5(<baseKey 4 little-endian><direction 1><randomPart 2>)
stream  = RC4(key)                 # 关键：不丢弃前 1024 字节
wire    = <marker 1><randomPart 2><ENC(magic 4)><ENC(padLen 1)><ENC(payload)>
magic   = 0x13EF24D5 (MAGICVALUE_UDP_SYNC_SERVER)
direction: 0x6B CLIENT->SERVER（发送），0xA5 SERVER->CLIENT（接收）
padLen  &= 0x0f；eMule 目前固定 0，但接受 1..15
```

**易错点**：eMule 的 `RC4CreateKey(..., bSkipDiscard=true)` 里的 `bSkipDiscard` 意为
「**跳过**丢弃步骤」（`otherfunctions.cpp:3676`：`if (!bSkipDiscard) RC4Crypt(NULL, NULL, 1024, key)`），
所以 UDP 信封**不**丢弃前 1024 字节。Go `crypto/rc4` 的 `NewCipher` 正好是这个行为；
若误读为「bSkipDiscard=true => 丢弃」，就会得到永远对不上的密流。这是本次专门核对的点。

eMule 的 `DecryptReceivedServer` 只尝试 `0xA5` 一个方向；我们为便于解码双向捕获的样本会尝试
`0xA5` 和 `0x6B` 两个方向，并以 `magic` 校验为准。这是**更宽松**而非不兼容，文档留档以便复核。

### 4.2 交叉验证方法

`internal/ed2k/protocol/server/emule_udp_vector_test.go` 与
`internal/ed2k/server_udp_encryption_test.go` 中各有一份**独立实现**的 eMule 封装/解封装函数，
不调用生产代码的 `EncryptServerDatagram` / `DecryptServerDatagram`：

| 测试 | 验证内容 |
|---|---|
| `TestDecryptServerDatagramAgainstEMuleServerVector` | eMule 形态（0xA5）的服务端包，padLen=0/5/15 都能被我们解出原文 |
| `TestEncryptServerDatagramMatchesEMuleDecoder` | 我们发出的包能被**只认 0x6B** 的 eMule 解码器还原，且不会误用服务端方向 |
| `TestDecryptServerDatagramRejectsInvalidEnvelope` | 错 key、截断包、明文透传 |
| `TestServerUDPEncryptedRoundTripAndEMuleAnswers` | 真实 UDP socket：发 `0x92` 加密请求 → eMule 解码器验证；回加密 `0x99`/`0x9B` → 回调收到解析结果 |

### 4.3 本轮新发现的缺陷：UDP key 绑定到了 `clientID`

eMule 的 `CServer::GetServerKeyUDP()`：

```cpp
return (m_dwIPServerKeyUDP != 0 && m_dwIPServerKeyUDP == theApp.GetPublicIP()) ? m_dwServerKeyUDP : 0;
```

`GetPublicIP()` 对 LOW_ID 客户端来自 `OP_IDCHANGE` 第 4 个字段（服务器报告的公网 IP，
`ServerSocket.cpp:383`），**不是** `clientID`（后者是服务器分配的 token）。原实现用
`keyIP == clientID` 校验，会导致防火墙后客户端收到的 key 一律被拒，UDP 加密永远不启用。

同时我们的 `IdChange` 只读了 12 字节，丢掉了第 4、5 个字段。现已修复：

- `IdChange` 解析 20 字节：`ReportedIP` / `ObfuscationTCPPort`；对 `IsLowID` 的 reported IP
  按 eMule 规则忽略；`Put`/`BytesCount` 同步。
- `Session.publicIP` 由 `OnServerIDChange` 写入（LOW_ID 取 reported IP，HIGH_ID 取 clientID）。
- `SetServerUDPKey()` 改为比对 `publicIP`，公网 IP 未知时拒绝安装（与 eMule 返回 0 一致）。

覆盖测试：`TestIdChangeParsesReportedPublicIP`、`TestIdChangeLegacyAndBogusReportedIP`、
`TestIdChangePutRoundTrip`、`TestOnServerIDChangeStoresReportedPublicIP`、
`TestSetServerUDPKeyUsesPublicIP`。

## 5. 复跑命令

```bash
# 协议层向量 + 真实回包黄金样本
go test ./internal/ed2k/protocol/server/ -run 'IdChange|EMule|UDPFoundSources|UDPGlobSearch' -v

# 真实 UDP socket 的加密往返 + 0x92 格式 + key 绑定
go test ./internal/ed2k/ -run 'TestUDPSearchReqV2WireFormat|TestServerUDPEncryptedRoundTripAndEMuleAnswers|TestSetServerUDPKeyUsesPublicIP' -v

# 真实公网服务器（需要可达的 eD2k 网络）
go run ./cmd/uded2kpeerprobe -server 57.128.161.24:6666 -search debian -debug -out dist/ed2k-interop/udp-search.json

# 全量
go test ./internal/... ./cmd/... && python tools/check_arch.py && python tools/build_docs.py --check
```

## 6. 结论与未闭环

**可以宣称**：eD2k server UDP 的三种搜索格式、两种查源格式、能力位时序与 RC4 信封，
在协议字节层面与 eMule srchybrid 一致，并有独立实现对端交叉验证；有真实公网服务器抓包证明
`0x90`→`0x99` 与 `0x94`→`0x9B` 两条链路可用。

**不能宣称**：与真实下发 `ST_UDPKEY` 的第三方服务器完成过加密端到端；`0x92` 分支在真实
服务器上跑过。这两项受限于公网服务器样本（40 台抽样仅 1 台有 UDP 能力，且不下发 key）。
若后续拿到具备 `SRV_UDPFLG_UDPOBFUSCATION` 且下发 `ST_UDPKEY` 的服务器，应补一次真机抓包，
确认 marker 不是协议头值、magic 与 padding 字段位置一致，再更新本节结论。

本报告的 PASS 项不改变 `12` §3.2 的 ED 门禁判定，也不翻转 P3 发布结论。
