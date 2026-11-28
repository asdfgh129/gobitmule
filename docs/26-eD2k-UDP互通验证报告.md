# 26 eD2k UDP 互通验证报告

> 文档编号：UD-DOC-26 ｜ 版本：v1.2 ｜ 状态：**验证报告（本地交叉验证 + 真实服务器抓包；不作发布门禁）** ｜ 日期：2026-11-28 ｜ 维护责任：架构组
>
> 本文只讨论 eD2k 的 **server UDP 数据报链路**（全局搜索、全局查源、能力协商与混淆信封）。
> TCP 登录/源交换、SecureIdent、KAD 的结论见 [`16`](16-互操作性与兼容性设计.md) §3.1 / §6.1。

## 1. 验证目标与边界

| 目标 | 结论 | 依据类型 |
|---|---|---|
| UDP 搜索 opcode 按服务器能力选择 | **PASS** | 真实服务器抓包 + 单测 |
| `0x90` tag set、`0x92` 裸搜索树、`0x98` legacy 三种格式 | **PASS（真机）** | 真实服务器抓包（`0x90`、强制 `0x92`）与 `0x98` 强制 opcode 往返（§4.5） |
| UDP 查源 `0x94` / `0x9A` 与 `0x9B` 回包解析 | **PASS（真机）** | `0x94` 抓包 + `0x9A` 强制 opcode 往返（§4.5）+ 黄金样本回归测试 |
| 抽样中 9 台 UDP 能力服务器的实际可达性 | **9/9 登录、9/9 UDP 搜索有回包；3/9 查源有回包** | 逐台真实服务器探针（§4.6） |
| 能力位时序（登录尚未拿到 `OP_SERVERIDENT` 时） | **PASS** | 真实服务器日志 + 单测 |
| RC4 信封与 eMule 字节级互认 | **PASS（交叉验证）** | 独立 eMule 算法实现的向量测试 + 真实 UDP socket 端到端 |
| RC4 信封在**真实服务器**上端到端（crypt ping 下发 key → 加密请求 → 加密回包） | **PASS（真机）** | Cyberchrist 18.1 `run13`/`run14` + `crypt-ping-cyberchrist2.pcapng` / `udp92-encrypted.pcapng` |
| `0x92` 分支在真实服务器上的往返 | **PASS（真机，强制 opcode）** | `run14`: `opcode=0x92` → 10 个加密 `0x99` 回包并解析入库 |

「PASS（交叉验证）」的含义：测试里的对端编码器是**按 eMule 源码另行实现**的，不复用我们的
`EncryptServerDatagram`/`DecryptServerDatagram`，因此共享实现错误不会让测试通过。
「PASS（真机）」另有真实公网服务器抓包与日志支撑，二者在下文分别标注，不可混用。

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

### 4.4 真机闭环：crypt ping 下发 key 与加密往返（2026-11-28）

`OP_SERVERIDENT` 只是 eMule 获取服务器 UDP key 的一条路径。eMule 还有第二条：
`CServerList::PingServers()` 先向服务器的 **TCP 端口 + 12** 发一个 obfuscated
servstat ping，`CUDPSocket::ProcessServerPacket()` 从 `OP_GLOBSERVSTATRES(0x97)`
包体偏移 36 取出 key 并调用 `CServer::SetServerKeyUDP()`。公开服务器常见的情况正是
`udpFlags` 宣告 `SRV_UDPFLG_UDPOBFUSCATION(0x200)`、但 `OP_SERVERIDENT` 里没有
`ST_UDPKEY`——Cyberchrist 18.1 就是如此，所以原先的加密链路在真机上永远不启用。

**本轮发现并修复的实现缺陷**：crypt ping 请求必须是**明文** `<challenge 4><padding 5>`。
eMule `CClientUDPSocket::SendPacket()` 对 raw packet 明确注释
`we don't encrypt rawpackets (!)`，只有已安装 server key 时才加密。我们最初把请求也套了 RC4
信封，服务器于是把密文前 4 字节当成 challenge 加密回包，客户端自然永远解不开。
第一次真机抓包 `crypt-ping-cyberchrist.pcapng` 帧 16/22 完整复现了这一点。
改为明文后，请求/应答立刻对上。

**真机日志**（`dist/ed2k-interop/run14.log`）：

```text
server udp -> crypt ping to 57.128.161.24:6678 challenge=877252234
server udp <- crypt ping answer challenge=877252234 key=1838661081 flags=0x17fb
server udp: obfuscation key installed keyIP=0
probe: server UDP key installed=true
server udp -> glob search "debian" to 57.128.161.24:6670 opcode=0x92 flags=0x73b ok=true
server udp search results got=1 added=1   （连续 10 条）
```

**抓包证据**（`dist/ed2k-interop/udp92-encrypted.pcapng`，本机 5512 ↔ 服务器 6670/6678）：

| 帧 | 方向 / 端口 | 长度 | 首字节与含义 |
|---|---|---|---|
| 18 | 本机 → 6678 | 9 B | `8ace4934 7c13006540` = 明文 challenge + 5 字节 padding |
| 24 | 6678 → 本机 | 62 B | `11 0d…` = 以 challenge 为 key 的加密 `0x97` 应答，解出 key `1838661081` |
| 28 | 本机 → 6670 | 19 B | `01 00 78 7b…` = 以 server key 加密的 `0x92` 搜索请求（marker 非协议头） |
| 32+ | 6670 → 本机 | 181/143/… B | 加密的 `0x99` 结果包；解密后 `0x99` 记录全部解析入库 |

此前 `run13` / `crypt-ping-cyberchrist2.pcapng` 已单独验证同一台服务器的 key 下发；
`run14` 把「等 key 安装完成再发请求」串起来，因此请求和回包都在信封里。

#### 4.4.1 0x92 真机往返

抽样中的 UDP 能力服务器都同时宣告 `EXT_GETFILES`+`LARGEFILES`，按 eMule 规则会自动选
`0x90`，无法自然走到 `0x92` 分支。为验证该格式本身能被真实服务器解析，探针新增
`-udp-search-opcode 0x92` 覆盖开关（默认关闭，不影响生产选择逻辑），在 key 安装后强制发送
裸搜索树。服务器连续回传 10 个加密 `0x99`，日志解析入库 10 条。

需要如实限定：这证明 **`0x92` 报文本身可被真实 eMule 系服务器接受并回传**，
不等于「找到一台自动选择 `0x92` 的服务器」；自动选择分支仍由
`TestUDPSearchReqV2WireFormat` / `TestGlobSearchOpcode` 单测覆盖。

#### 4.4.2 新增测试

- `TestCryptPingInstallsKeyThenEncryptsRequests`：真实 UDP socket 上跑通
  明文 ping → challenge 加密的 `0x97` → key 安装 → 后续请求用新 key 加密；
  对端解码器是独立的 eMule 实现，不复用生产函数。
- `TestParseGlobServStatRes` 系列：`0x97` 短包/完整包解析，key 取自 body[36:40]。
- 探针 `-wait-udp-key` 让真机复跑能稳定等到信封激活后再发请求。

### 4.5 legacy opcode 真机往返：`0x98` 与 `0x9A`

此前的未闭环项是「两种 legacy 格式只做过单测，没有跑真实服务器」。本轮给探针补了
`-udp-only` 模式：只登录、只发 UDP 请求并统计**解出的**回包，不加载 transfer、
不依赖 peer 状态，避免 peer 连接掩盖 UDP 是否真的回包。新增开关：

| 开关 | 作用 |
|---|---|
| `-udp-only` | 跳过 peer swarm，只做 UDP 证据采集 |
| `-udp-search` / `-udp-source` | 分别启用全局搜索 / 全局查源 |
| `-udp-search-opcode 0x98` / `-udp-source-opcode 0x9A` | 强制 legacy opcode，生产默认仍按能力位自动选择 |
| `-udp-wait N` | 发完请求后等待 N 秒收集回包 |

会话层相应增加 `SetForcedUDPSourcesOpcode()`（原来只有搜索 opcode 可覆盖）与
`UDPTrafficStats()`：接收路径按 `0x99` / `0x9B` 解析成功计数，并逐条打印
`server udp source answer hash=... got=N`，这样才能把回包归属到目标哈希，而不是把
登录时其他哈希的异步回包误算进来。单测 `TestForcedUDPSourcesOpcodeOnRealSocket`
在真实 UDP socket 上断言 `0x9A`（18 B）与 `0x94`（22 B）的字节内容，并验证强制值
能绕过「无能力位就不发」的默认分支。

**`0x98` 真机**（`dist/ed2k-interop/udp98-real.log/.json`，Cyberchrist 18.1）：
crypt ping 用 challenge `145054978` 拿到 key `1838661081`，随后强制发
`0x98`，服务器回传 **10 个加密 `0x99`，解析出 10 条结果**：

```text
probe: forcing UDP search opcode 0x98
server udp -> crypt ping to 57.128.161.24:6678 challenge=145054978
server udp <- crypt ping answer challenge=145054978 key=1838661081 flags=0x17fb
server udp -> glob search "debian" to 57.128.161.24:6670 opcode=0x98 flags=0x73b ok=true
（10 × server udp search results got=1 added=1）
```

**`0x9A` 真机**（`dist/ed2k-interop/udp9a-real.log/.json`，同服务器）：
crypt ping 用 challenge `2626736283` 拿到同一 key，强制发裸 hash 的 `0x9A`，
服务器回传 **1 个加密 `0x9B`**，逐哈希日志确认目标是 `1565D4…C90`：

```text
probe: forcing UDP source opcode 0x9a
server udp -> glob sources 1565D4FAE4B5F20EC77429622BC46C90 to 57.128.161.24:6670 opcode=0x9a ok=true flags=0x73b
server udp source answer hash=1565D4FAE4B5F20EC77429622BC46C90 got=1 from=57.128.161.24:6670 encrypted=true
```

因此两种 legacy 报文本身都能被真实 eMule 系服务器解析并回传。边界与 `0x92` 相同：
这是**强制 opcode** 验证格式，不是「找到一台会自动选择 legacy 的服务器」。

### 4.6 抽样 9 台 UDP 能力服务器逐台验证

`dist/ed2k-interop/server-capabilities.txt` 中 9 台宣告过非零 `ST_UDPFLAGS` 的服务器
全部重跑：登录 → 等 `OP_SERVERIDENT` → 若有 `SRV_UDPFLG_UDPOBFUSCATION` 则 crypt ping
装 key → 搜 `debian` → 用已知 ISO 哈希（`1565D4FAE4B5F20EC77429622BC46C90`，
663748608 B）查源。证据在 `dist/ed2k-interop/udp-cap/*.json/.log`。

| 服务器 | 实时 ident flags | crypt key | `0x99` 包/条目 | 目标哈希 `0x9B` 回包 | 结论 |
|---|---|---|---|---|---|
| 85.121.5.137:4232 | `0x00000000` | 否 | 10/10 | 0 | 搜索 OK，查源无回包 |
| 85.137.248.210:56612 | `0x0030573b` | 否 | 8/73 | 0 | 搜索 OK，查源无回包 |
| 91.208.162.55:4235 | `0x0010073b` | 是 | 10/10 | 0 | 搜索 OK，查源无回包 |
| 91.208.162.87:4232 | `0x0010073b` | 是 | 10/10 | 1 | 搜索、查源均 OK |
| 82.192.72.186:4661 | `0x0010073b` | 否 | 7/7 | 1 | 明文路径下搜索、查源均 OK |
| 77.42.68.79:4232 | `0x00000000` | 否 | 10/10 | 0 | 搜索 OK，查源无回包 |
| 91.208.162.182:4232 | `0x0010073b` | 是 | 10/10 | 0 | 搜索 OK，查源无回包 |
| 57.128.161.24:6666 | `0x0000073b` | 是 | 10/10 | 1 | 搜索、查源均 OK |
| 143.246.192.192:4232 | `0x0010073b` | 是 | 8/8 | 0 | 搜索 OK，查源无回包 |

汇总口径：**9/9 登录成功、9/9 UDP 搜索收到并解析出结果、5/9 通过 crypt ping 装上了
server key、3/9 对目标哈希回传了 `0x9B`**。探针的总 verdict 对「搜索 + 查源」取与，
所以上表 6 台记为 FAIL，仅表示其中查源没有回包，不代表 UDP 链路不可用。

两点需要留档：

1. 85.121.5.137 与 77.42.68.79 的实时 `OP_SERVERIDENT` 这次报 `udpFlags=0`，
   但 `server.met` 缓存的非零值让请求在 ident 到达前就选了 `0x90`，服务器仍然回了
   结果。这验证了 §3.3 的预热策略；同时实时 ident 仍以实况为准更新显示值。
2. `82.192.72.186` 没有 crypt key，搜索和查源都在**明文**下完成，说明加密信封不是
   这 9 台服务器回包的必要条件；能否装 key 由 `SRV_UDPFLG_UDPOBFUSCATION` 与 `0x97`
   应答共同决定。

没拿到 `0x9B` 的 6 台不能据此判定实现不兼容：同一批请求里 `0x94` 的字节格式与在
Cyberchrist / Sharing-Devils No.4 上成功的请求完全一致，差异更可能是目标哈希在该
服务器的索引里没有可用源。这里只记录「未回包」，不外推结论。

## 5. 复跑命令

```bash
# 协议层向量 + 真实回包黄金样本
go test ./internal/ed2k/protocol/server/ -run 'IdChange|EMule|UDPFoundSources|UDPGlobSearch|ParseGlobServStatRes' -v

# 真实 UDP socket 的加密往返 + 0x92/0x9A 格式 + key 绑定
go test ./internal/ed2k/ -run 'TestUDPSearchReqV2WireFormat|TestForcedUDPSourcesOpcodeOnRealSocket|TestServerUDPEncryptedRoundTripAndEMuleAnswers|TestSetServerUDPKeyUsesPublicIP|TestCryptPingInstallsKeyThenEncryptsRequests' -v

# 真实公网服务器：等 crypt ping 装好 key，再强制 0x92 验证加密往返
go run ./cmd/uded2kpeerprobe -server 57.128.161.24:6666 -search debian \
  -wait-udp-key -udp-search-opcode 0x92 -debug -out dist/ed2k-interop/udp92-encrypted.json

# legacy 真机往返：0x98 搜索、0x9A 查源
go run ./cmd/uded2kpeerprobe -server 57.128.161.24:6666 -search debian \
  -udp-only -udp-search -udp-search-opcode 0x98 -wait-udp-key -udp-wait 8 \
  -debug -out dist/ed2k-interop/udp98-real.json
go run ./cmd/uded2kpeerprobe -server 57.128.161.24:6666 \
  -udp-only -udp-search=false -udp-source -udp-source-opcode 0x9A \
  -hash 1565D4FAE4B5F20EC77429622BC46C90 -size 663748608 \
  -wait-udp-key -udp-wait 10 -debug -out dist/ed2k-interop/udp9a-real.json

# 抽样 9 台 UDP 能力服务器逐台验证（逐台串行，避免端口冲突）
awk -F'\t' '/udpFlags=0x/ && $0 !~ /udpFlags=0x00000000/ {print $1}' \
  dist/ed2k-interop/server-capabilities.txt | while read -r srv; do
  go run ./cmd/uded2kpeerprobe -server "$srv" -search debian -search-wait 8 \
    -udp-only -udp-search -udp-source \
    -hash 1565D4FAE4B5F20EC77429622BC46C90 -size 663748608 \
    -wait-udp-key -udp-wait 6 -debug \
    -out "dist/ed2k-interop/udp-cap/${srv//:/_}.json"
done

# 全量
go test ./internal/... ./cmd/... && python tools/check_arch.py && python tools/build_docs.py --check
```

## 6. 结论与未闭环

**可以宣称**：eD2k server UDP 的三种搜索格式、两种查源格式、能力位时序与 RC4 信封，
在协议字节层面与 eMule srchybrid 一致，并有独立实现对端交叉验证。真实公网服务器抓包证明：
`0x90`→`0x99`、`0x94`→`0x9B` 可用；crypt ping 从 `OP_GLOBSERVSTATRES` 拿到 server key，
其后 `0x92` 请求与 `0x99` 回包都以 RC4 信封传输并被正确解析（`run14` /
`udp92-encrypted.pcapng`）。本轮又补齐 `0x98`→`0x99`（10 条）与 `0x9A`→`0x9B`（1 个目标
哈希回包）的强制 opcode 真机往返，并把抽样中 9 台服务器全部重跑：9/9 登录、
9/9 UDP 搜索有回包、3/9 目标哈希查源有回包（§4.5 / §4.6）。

**仍不能宣称**：找到一台会**自动选择** `0x92` 或 `0x9A` 的服务器——抽样里的服务器要么
同时宣告 `EXT_GETFILES`+`LARGEFILES` 自动走 `0x90`，要么宣告 `EXT_GETSOURCES2` 自动走
`0x94`；`0x92`/`0x9A`/`0x98` 是用覆盖开关验证报文格式本身。也不能宣称 6 台查源未回包
的服务器「不兼容」：只记录未回包，不外推原因。`0x97` 之外其他下发 key 的服务器实现
仍未覆盖。早前记录的「40 台抽样仅 1 台有 UDP 能力」是按 8 位零值匹配 grep 漏判，
实际是 9 台（见 `dist/ed2k-interop/server-capabilities.txt`），其中没有一台在
`OP_SERVERIDENT` 里带 `ST_UDPKEY`；本轮 5/9 是通过 crypt ping 的 `0x97` 拿到 key。

本报告的 PASS 项不改变 `12` §3.2 的 ED 门禁判定，也不翻转 P3 发布结论。
