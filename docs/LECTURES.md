# 课程协议与观察场景（L05–L07）

《操作系统与分布式系统》L05《Distributed Synchronization (1)》、L06《Distributed Synchronization (2)》、L07《Replication and Consensus》讲到的协议，全部在 `protocols/` 下实现为独立的 Go net/rpc 项目。每个目录包含：

- `main.go` / `protocol.go`：协议实现（节点运行在真实 Docker 容器中）；
- `README.md`：课件页码、算法要点、节点状态字段、应用输入、各观察场景的操作步骤与思考题、实现边界；
- `scenarios.json`：观察场景的机器可读版本，每个场景对应平台上的一个实验，并带有断言，同时是自动化测试。

## 快速开始

```bash
npm start                                   # 启动平台
node scripts/scenarios.mjs list             # 列出全部协议与场景
node scripts/scenarios.mjs setup            # 在平台创建全部协议和场景实验（不运行）
node scripts/scenarios.mjs run paxos-basic case4-unseen   # 自动回放一个场景并检查断言
node scripts/scenarios.mjs run paxos-basic --keep         # 回放后不结束运行，便于继续手动操作
```

`setup` / `run` 以目录代码为准：若平台上的协议代码与目录不同，会用目录代码更新平台协议（历史运行的代码快照不受影响）。在平台上修改过的代码请先拷回目录。

课堂上可以两种方式使用：

1. **手动演示**：打开「我的协议 → 协议 → 实验列表」中对应场景的实验，点「运行实验」，按 README 的步骤点击节点发送应用输入、注入故障，边操作边讲。
2. **自动回放**：先用 `run` 跑一遍，再在实验的「运行历史」中回放，暂停、单步查看时空图与每个节点的状态。

## 平台上与课件相关的两个功能

- **并发输入**：点击节点 → 应用输入 → 勾选「同时发送到」其他节点，同一操作在同一协调器时刻送达所有选中节点，它们彼此没有因果关系（课件中的「concurrent events」）。场景文件中用 `{"concurrent":[{node,method,values},...]}` 步骤。
- **指数分布网络抖动**：实验参数「延迟分布」选「固定延迟 + 指数分布抖动」，每条消息的延迟为 latency + Exp(均值)；也可以在链路面板中对单条链路设置。每条有向链路仍保持 FIFO（同 TCP），抖动不会造成乱序。

## 协议与场景一览

### L05 时间与时钟

| 目录 | 协议 | 场景 |
| --- | --- | --- |
| `clock-cristian` | Cristian 时钟同步（p.12–14） | 对称延迟一次同步；非对称链路；周期同步抑制漂移；服务器排队延迟 |
| `clock-ntp` | NTP：t0–t3、offset/delay、8 次测量取最小 delay、stratum（p.15–21） | 服务器处理时间被扣除；抖动与最小 delay 过滤；非对称链路仍有误差；误差随 stratum 累积 |
| `clock-berkeley` | Berkeley 算法（p.22–26） | 周期轮次收敛；离群时钟不参与平均；跳变 vs 平滑调整（时间倒退）；master 崩溃 |
| `logical-clocks` | Lamport 时钟 + 向量时钟 + 全序时间戳（p.28–46） | 课件图 L(b)>L(e) 但 b∥e；跨三节点因果链；并发事件 Lamport 值相等；随机事件后比较 |

时钟同步协议中每个节点模拟自己的硬件时钟（初始偏移 + 放大的漂移率，可用 `SetClock` 修改），因为容器共享宿主机时钟；`errorMs` 是本地时钟与真实时间之差。

### L06 互斥、选举与全序组播

| 目录 | 协议 | 场景 |
| --- | --- | --- |
| `to-multicast` | 全序组播，复制银行账户（p.2–7） | naive 副本分歧 1111/1110；ordered 同序交付；N=4 的消息开销；一个节点崩溃全体停止交付；崩溃恢复后继续 |
| `mutex-central` | 集中式互斥（p.12–16） | FIFO 排队与每轮 3 条消息；协调者崩溃阻塞；协调者重启失忆导致两人同时持锁；持久化后安全 |
| `bully-election` | Bully 选举（p.18–22） | 启动选出最大编号；协调者崩溃次高接任；课件 7 节点示例；旧协调者恢复后抢回；最高两个节点同时崩溃 |
| `mutex-majority` | 完全去中心化多数投票（p.24–26） | 无竞争一次成功；全体同时申请分票、退避、重试；投票者重启遗忘选票导致两人同时进入；持久化选票后安全 |
| `mutex-lamport` | Lamport 互斥（p.27–31） | 两节点同时请求；复制的优先队列按时间戳排序；任一节点崩溃阻塞 |
| `mutex-ricart-agrawala` | Ricart & Agrawala（p.32–43） | 两节点同时请求；课件例子（时间戳而非先后）；无关节点崩溃也阻塞；5 节点随机负载；指数抖动网络下的随机负载 |
| `netrpc-token`（平台原有示例） | 令牌环（p.44–46） | 无场景文件；可手动演示令牌传递，以及崩溃持有者后令牌丢失 |

四种互斥算法（集中式、多数投票、Lamport、R&A）使用相同的应用输入 `Acquire(holdMs)`、`Auto(...)` 和相同的状态字段（`role`、`entries`、`lastWaitMs`、`messagesSent`），可以对照课件 p.47 的比较表。

### L07 复制与共识

| 目录 | 协议 | 场景 |
| --- | --- | --- |
| `primary-backup` | 主备复制 + 一致性模型（p.11、p.70–81） | 异步复制过期读、主节点读、会话读（zxid）；同步复制与备份崩溃；备份被分区 |
| `quorum-kv` | Quorum 复制 N/W/R 与 CAP（p.5–7、p.12–14） | 避开最慢副本；W+R≤N 读到旧值；网络分区时少数派不可用（CP） |
| `paxos-basic` | Basic Paxos（p.21–49） | 情形 1–4（与课件图一致）；活锁与随机退避；重启丢失投票导致选出两个值；持久化后安全 |
| `paxos-kv` | 每个日志槽一次 Paxos 的复制状态机 KV（p.51–56） | 依次写入与读取；两个写入竞争同一槽位；少数节点崩溃与恢复追赶；少数派分区无法提交 |
| `two-phase-commit` | 两阶段提交（p.59–67） | 转账提交；参与者否决；参与者投票前崩溃；协调者决定前崩溃（阻塞）；协调者提交点后崩溃 |

## 场景文件格式

```json
{"scenarios":[{
  "id":"concurrent", "name":"R&A · 两个节点同时请求",
  "settings":{"nodeCount":3,"latency":400,"delayModel":"exponential","jitter":100},
  "description":"…",
  "steps":[
    {"concurrent":[{"node":"node-1","method":"Application.Acquire","values":{"holdMs":3000}},
                   {"node":"node-3","method":"Application.Acquire","values":{"holdMs":3000}}],"await":false},
    {"input":{"node":"node-2","method":"Application.Tick","values":{"events":11}}},
    {"fault":{"kind":"link","from":"node-1","to":"node-2","latency":3000}},
    {"fault":{"kind":"crash","node":"node-2"}},
    {"wait":2000},
    {"until":{"node":"node-1","path":"entries","value":1},"timeout":20000,"label":"node-1 进入临界区"},
    {"check":{"atMostOne":{"path":"role","value":"critical"}},"label":"互斥始终成立"}
  ]}]}
```

- `input` / `concurrent`：按 `method`（RPC 标签，如 `Application.Acquire`）找到节点声明的动作；`await:false` 不等待返回；`expect` 可为 `ok`、`error`、`timeout`。
- `fault`：与平台故障 API 相同；link 省略的字段取实验默认值，`blocked`/`bidirectional` 默认 false。
- 条件：`{node, path, op, value}`（node 可为 `"*"` 全部在线节点、`"any"` 或数组；op 支持 `== != < <= > >= abs<= abs> includes exists missing in`）、`{same: path}`、`{differ: path}`、`{messages:{method, op, value}}`（RPC 请求计数）、历史不变式 `{atMostOne:{…}}` / `{moreThanOne:{…}}`，以及 `all` / `any` / `not` 组合。

## 已知平台特性

- 协调器每 50 ms 推进一次消息投递，实际延迟比设定值早到最多约 50 ms；阈值断言应留出余量。
- 节点崩溃后，节点面板和场景条件仍显示它最后上报的状态，直到恢复后的进程重新上报；需要判断「已重启」的场景用协议自己上报的字段（如 `incarnation`、`recovered`）。
- 同一时间只允许一个运行；`run` 会自动等待其他运行结束，不会停止别人的运行。
