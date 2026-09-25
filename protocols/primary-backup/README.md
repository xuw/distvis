# 主备复制与一致性模型

课件：L07《Replication and Consensus》第 11 页（Primary-Backup），第 70–81 页（一致性模型：线性一致、顺序一致、读己之写 / zxid）。

## 算法

- node-1 是主副本（`primary`），其余是备份（`backup`）。写请求可以发到任何节点，备份用 `PB.Put` 转发给主副本。
- 主副本为每个写分配递增的日志序号 `index`（相当于课件中的 zxid），立即应用到本地，再由每个备份一个的发送协程**按序**用 `PB.Replicate` 发送；备份只接受 `index = 已应用 + 1` 的条目，返回自己的 `applied`，主副本据此从缺口处重发（分区或崩溃后追日志）。
- 复制模式（`Application.Mode`）：
  - `sync`：主副本等**所有**备份确认后才回复客户端（课件：must wait for all backups）。任一备份不可达时写入在 4 秒后以错误返回——可用性的代价。
  - `async`：主副本应用后立即回复；备份在额外延迟 `replicationDelayMs` 后才收到更新，让过期读可以被观察到。
- 读（`Application.Get`）的三种方式：
  - `local`：读本副本，快，但可能过期——不满足线性一致性；
  - `primary`：转发到主副本读，线性一致（所有写都在主副本排序）；
  - `session`：客户端带上自己上一次写返回的 `index` 作为 `minIndex`，副本等本地日志追上后再读——读己之写、不会「倒退」（课件第 80–81 页：client knows it needs to wait until log catchup）。

时空图中 `调用 PB.Replicate · {"i":2,"k":"x","v":"1"}` 是一条日志条目，返回 `{"applied":2}` 是备份的确认。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` | `primary` / `backup` |
| `mode` | `sync` / `async`；async 时另有 `replicationDelayMs` |
| `appliedIndex` | 本副本已应用的日志长度 |
| `store` | 本副本的键值（由日志重放得到） |
| `acked` | 仅主副本：每个备份确认到的 index |
| `lastRead` | 本节点最近一次 Get：`key, mode, value, found, index`（读时本副本的 appliedIndex）、`stale`、`waitedMs` |

`stale` 的含义：本副本**已知**存在但尚未应用的写（它转发过的写返回的 index、会话读给出的 minIndex、或收到的复制消息）。副本无法知道自己没听说过的写，所以分区中的备份读到旧值时 `stale` 仍可能是 false——这正是本地读危险的地方。

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.Put(key, value)` | 写入；在备份上调用会转发给主副本。返回 `index` |
| `Application.Get(key, mode, minIndex)` | `mode` = `local`（默认）/ `primary` / `session`；session 读等本地 appliedIndex ≥ minIndex，最多 5 秒 |
| `Application.Mode(mode, replicationDelayMs)` | 切换 `sync` / `async`，广播给所有节点 |

## 观察场景

每个场景都已作为一个实验创建（`node scripts/scenarios.mjs setup primary-backup`），可手动操作，也可用 `node scripts/scenarios.mjs run primary-backup <场景ID>` 自动回放后在「运行历史」中逐步查看。

### async-stale · 异步复制下的过期读与会话读（3 节点）

1. `Mode(async, 4000)`：备份 4 秒后才收到更新。
2. 在 node-2 上 `Put(x, 1)`，写已经返回成功（index 1）；紧接着在 node-2 上本地读：读不到 x，`stale=true`。
3. 同一个客户端改为 `primary` 读：读到 1。
4. 在 node-3 上 `Put(x, 2)` 得到 index 2，再用 `session` 读并带上 `minIndex=2`：约 4 秒后返回 2（`waitedMs` 记录了等待时间）。
5. 最终所有副本一致（最终一致）。

观察：第 2 步的写在第 3 步的读之前完成，却读到了旧值——违反线性一致的实时顺序（课件第 75 页）。思考：session 读保证了什么，没有保证什么？另一个客户端在 node-2 读 x 会怎样？

### sync-blocks · 同步复制与备份崩溃（3 节点）

1. `Mode(sync)`；在 node-3 上写 x=1，写返回后 node-2 本地读就是新值。
2. 崩溃 node-3，再写 x=2：主副本等不到 node-3 的确认，约 4 秒后返回错误。
3. 但主副本和 node-2 已经应用了 x=2——客户端看到「失败」，数据却已部分生效。
4. 恢复 node-3，它从 index 2 追上；之后同步写入恢复可用。

思考：失败的写该如何对客户端解释？（超时 ≠ 没执行）如果此时主副本崩溃、node-2 接任，x=2 算不算已写入？

### partition · 备份被分区（3 节点）

1. `Mode(async, 0)`，断开 node-1 ↔ node-3。
2. 写入照常成功（主副本和 node-2），node-3 本地读返回旧值，而且它不知道自己过期。
3. node-3 上的会话读（minIndex=1）等不到日志、node-3 上的写无法转发到主副本，都返回错误。
4. 网络恢复后 node-3 从缺口处追上。

思考：对照 CAP（第 5–7 页）：async + local 读选择了什么？sync 模式下同一个分区会发生什么？

## 实现边界

- **没有自动故障转移**：主副本固定为 node-1，它崩溃期间写入与 `primary` 读都不可用。主副本由谁担任、如何保证旧主不再接受写（避免脑裂）是共识问题，见 Paxos / Raft。
- 日志与模式用 `sdk.Save` 持久化，崩溃恢复后从日志重建 `store`；主副本重启后从各备份的 `applied` 继续发送。
- 同步模式下写入超时后，主副本已经应用的条目不会回滚，最终仍会复制到所有备份。
- 每条 RPC 只带一个日志条目，便于在时空图中观察；真实系统会批量发送。
