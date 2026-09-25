# 集中式互斥

课件：L06《Distributed Synchronization (2)》第 12–16 页；算法对比表第 47 页。

## 算法

- node-1 是协调者（`role=leader`），只管理锁，不申请锁；node-2 … node-N 是客户端。
- 客户端申请：发送 `Coord.Request{from}`，然后等待 Grant。
- 协调者收到请求：锁空闲则立即发送 `Client.Grant`；否则把请求者追加到 FIFO 队列 `queue`。
- 客户端离开临界区：发送 `Coord.Release{from}`；协调者从队首取出下一个客户端并发送 Grant。
- 每轮（一次进入+离开）3 条消息：Request、Grant、Release。RPC 的「返回」只是传输层确认，不算协议消息（Request 的返回 `queued` 表示排在第几位，0 表示已被授予）。
- 安全性显然成立（只有协调者发 Grant）；公平性取决于排队策略，FIFO 即公平。

### 故障相关的实现选择

- 客户端的 Request / Release 发送失败（协调者宕机）时每秒重试；协调者对重复 Request 去重、对非持有者的 Release 忽略。
- 协调者状态（持有者、队列）默认只在内存中。`Application.Persist(enabled=true)` 后每次变更都用 `sdk.Save` 落盘，重启时读回，并向持有者补发一次 Grant（客户端若已离开会回复 `ok=false`，协调者据此把锁交给下一位）。

## 节点状态

| 字段 | 节点 | 含义 |
| --- | --- | --- |
| `role` | 全部 | 协调者为 `leader`；客户端为 `released` / `wanted` / `critical` |
| `holder` | 协调者 | 当前持锁者，空串表示空闲 |
| `queue` | 协调者 | 等待中的客户端（FIFO） |
| `grantOrder` | 协调者 | 本进程生命期内的授予顺序 |
| `persist` / `bootState` | 协调者 | 是否持久化；启动时状态 `fresh`（空白）或 `restored`（从磁盘读回） |
| `entries` | 客户端 | 进入临界区次数 |
| `lastWaitMs` | 客户端 | 最近一次从申请到进入的等待时间 |
| `messagesSent` | 全部 | 本节点发出的 Request / Grant / Release 数量 |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.Acquire(holdMs)` | （客户端）申请临界区，进入后停留 holdMs 毫秒（默认 2000）再自动释放 |
| `Application.Auto(enabled, intervalMs, holdMs)` | （客户端）随机负载：按平均间隔反复申请 |
| `Application.Persist(enabled)` | （协调者）开启/关闭状态持久化 |

## 观察场景

`node scripts/scenarios.mjs run mutex-central [场景ID]` 自动回放并检查。

### fifo · FIFO 排队与每轮 3 条消息（5 节点，延迟 200ms）

1. node-4 申请并持锁 3 秒。
2. node-2、node-5、node-3 每隔 300ms 依次申请；协调者队列为 `[node-2, node-5, node-3]`。
3. 锁依次交给 node-2 → node-5 → node-3（按到达顺序，而不是编号）。
4. 全部结束后，Request、Grant、Release 各 4 条。

思考：如果协调者总是优先授予编号最小的节点，会发生什么？（课件：1、2 可以让 3 饿死）

### crash-blocks · 协调者崩溃使等待者阻塞（3 节点）

1. 协调者开启持久化；node-2 持锁，node-3 排队。
2. 协调者崩溃。node-2 离开临界区，但 Release 送不到（时空图中周期重试）；node-3 一直 `wanted`——锁其实已经空闲。
3. 协调者恢复，从磁盘读回持有者与队列，收到重试的 Release 后把锁交给 node-3。

思考：客户端无法区分「协调者崩溃」和「前面排队的人多」，它应该设多长的超时？超时后又能做什么？

### reboot-amnesia · 协调者重启失忆（3 节点）

1. node-2 持锁（45 秒）。协调者崩溃后立即重启，`holder` 变成空串。
2. node-3 申请，立刻得到 Grant：node-2 和 node-3 同时处于 `critical`。检查项用 `moreThanOne` 断言了这次违反互斥。

思考：课件问「协调者重启会怎样？（有状态）」——哪些状态必须持久化？只持久化 holder 够不够？

### reboot-persist · 持久化后重启仍然安全（3 节点）

与上一场景相同，但先 `Persist(enabled=true)`：重启后 `bootState=restored`、`holder=node-2`，node-3 只能排队，直到 node-2 释放。

思考：每次变更都要同步写盘，这对协调者的吞吐量有什么影响？

## 实现边界

- 客户端崩溃（持锁者崩溃后锁永不归还）不在本实验范围。
- 持久化只保存协调者的 holder 与 queue；重启后的 `grantOrder` 重新开始计数。
- 节点 1 的角色固定为协调者，没有协调者选举（见 Bully 选举实验）。
