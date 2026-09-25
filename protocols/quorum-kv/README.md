# Quorum 复制（N / W / R）

课件：L07《Replication and Consensus》第 12–14 页（Quorum based replication），第 5–7 页（CAP）。

## 算法

- N 个节点都是副本，任一节点都可以作为协调者处理客户端的读写；协调者自己也算一个副本。
- 版本号 `c.n`：`c` 是 Lamport 计数器（每见到一个版本就取最大值），`n` 是协调者编号，用于打破平局。副本只保留版本更大的值。
- `Put(key, value)`：协调者生成比自己见过的都大的版本，本地写入并向其余 N-1 个副本发 `Q.Store`，收到 **W** 个确认（含自己）即返回；其余请求继续在后台完成。
- `Get(key)`：协调者读本地并向其余副本发 `Q.Fetch`，收到 **R** 个回复（含自己）即返回其中版本最高的值。
- **W + R > N** 时，任意写法定人数与读法定人数至少有一个公共副本，所以读一定能看到最近一次成功的写（第 12–13 页）。
- 法定人数凑不齐时，协调者最多等 5 秒就返回错误 `only k/W acks` / `only k/R replies`，不等默认 10 秒的 RPC 超时。
- Get 返回后，协调者继续收集 6 秒内到达的其余回复：若有副本的版本更高，就在 `lastGet.stale` 中标记这次读过期；开启 `readRepair` 时把最新版本写回回复了旧版本的副本。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` | 所有节点都是 `replica` |
| `n` / `w` / `r` | 副本数与读写法定人数 |
| `overlap` | W+R>N 是否成立 |
| `readRepair` | 是否开启读修复 |
| `counter` | Lamport 计数器 |
| `store` | `key → {value, version}` |
| `lastPut` | 本节点最近一次成功的写：`version`、`acks`（确认的副本，按到达顺序）、`ms` 耗时 |
| `lastGet` | 本节点最近一次读：`value`、`version`、`from`（参与读法定人数的副本）、`ms`、`stale`、`latest`（事后看到的最高版本） |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.SetQuorum(w, r, readRepair)` | 设置 W、R 与读修复，广播给所有协调者（默认 W=R=⌊N/2⌋+1） |
| `Application.Put(key, value)` | 以本节点为协调者写入 |
| `Application.Get(key)` | 以本节点为协调者读取 |

## 观察场景

每个场景都已作为一个实验创建（`node scripts/scenarios.mjs setup quorum-kv`），也可用 `node scripts/scenarios.mjs run quorum-kv <场景ID>` 自动回放后在「运行历史」中查看。

### slow-replica · 避开最慢的副本（5 节点）

1. W=3、R=3；node-1→node-5 链路延迟 3000ms。
2. node-1 写 x=1：约 0.2 秒返回，`acks` 中没有 node-5；node-5 约 3 秒后才收到。
3. node-1 读 x：同样只等最快的 3 个副本。
4. 改为 W=5 后写 y：每次写都要等最慢的 node-5（>3 秒）。

思考：第 14 页「Avoid the slowest node (long tail)」——W、R 越小越快，代价是什么？

### weak-quorum · W+R≤N 读到旧值（3 节点）

1. W=1、R=1，断开 node-1→node-3 单向链路。
2. node-1 写 x=1 立即返回（只需自己确认）；node-3 读 x 只问自己，读不到——一次已成功的写被「看不见」，稍后迟到的 node-2 回复把 `lastGet.stale` 标为 true。
3. 改为 W=2、R=2（开启读修复）：node-1 写 x=2 需要 node-2 确认；node-3 读时读法定人数必含 node-2，读到 2；读修复把 2 写回 node-3。

思考：W=1、R=3 或 W=3、R=1 各适合什么负载？

### partition · 网络分区（CP，5 节点）

1. W=3、R=3，先写 x=1 到全部副本。
2. 分区 {node-1, node-2} | {node-3, node-4, node-5}。
3. 少数派：node-1 写 x=2 失败（只有 2/3 个确认），node-2 读也失败——保持一致但不可用。
4. 多数派：node-4 写 x=3、node-5 读到 3。
5. 注意：失败的写 x=2 仍然残留在 node-1、node-2 上。
6. 分区恢复后，node-1、node-2 读 x 都得到 3：任何 3 个副本里至少有一个来自多数派，而多数派写入的版本 `2.4` 比残留的 `2.1` 大。

思考：对照第 7 页 CP / AP：如果把 W、R 设成 1，分区时会怎样？失败的写为什么不能简单视为「没有发生」？

## 实现边界

- 这里的 quorum 复制只保证「读到最近一次**成功**的写」。并发写按版本号「后写者胜」；版本号来自协调者自己的计数器，没有写前读版本的阶段（ABD 算法），所以两个协调者的并发写可能与实时顺序不符，也不提供读写的线性一致。
- 失败的写不会回滚，可能在之后被读到或被读修复传播（第 5 步）。
- 副本状态只在内存中，崩溃恢复后从空状态开始（场景中未使用崩溃）；没有反熵（anti-entropy）和 hinted handoff，落后的副本只靠后续写入或读修复追上。
- 成员固定为全部 N 个节点，没有 sloppy quorum。
