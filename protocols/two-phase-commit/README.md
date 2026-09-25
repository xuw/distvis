# 两阶段提交（2PC）

课件：L07《Replication and Consensus》第 59–67 页。

## 算法

- node-1 是协调者（TM），其余节点是参与者（RM），各持有一个初始余额 100 的账户，账户名就是节点 ID。
- `Transfer(from, to, amount)` 开启事务 `T1, T2…`，参与者是 from 和 to 两个节点。协调者先把事务记入日志（`preparing`）。
- **阶段 1**：协调者向两个参与者发 `RM.Prepare{txn, delta}`。参与者校验（余额够不够、账户是否被别的事务锁住、是否被强制否决），投 YES 前把 `prepared` 记录写入稳定存储（`sdk.Save`）并锁住账户；投 NO 则直接中止。3 秒没有投票视为 NO。
- **阶段 2**：全体 YES 才提交，否则中止——任何一方都能否决，多数不够（第 60 页）。协调者**先把决定写入日志（提交点）**，再通知参与者：`RM.Commit` 重试直到每个参与者确认；`RM.Abort` 只发一次（推定中止）。
- **参与者的终结协议**：处于 `prepared` 超过 3 秒还没收到结果，就每 2 秒用 `TM.Status` 询问协调者；协调者不可达时变为 `uncertain`——既不能提交也不能中止，只能持锁等待（第 65–66 页）。
- **协调者恢复**：重启后读日志——没有决定的事务一律中止（推定中止，presumed abort），已决定提交但未全部确认的重新发送 Commit；对日志中没有的事务，`TM.Status` 回答 `aborted`。

时空图中 `调用 RM.Prepare · {"txn":"T1","delta":-30}` 的返回 `{"vote":"yes"}` 或 `{"vote":"no","why":"balance 100"}` 就是投票。

## 节点状态

协调者（node-1）：

| 字段 | 含义 |
| --- | --- |
| `role` | `coordinator` |
| `txns` | 日志中的事务：`T1 → preparing / committed / aborted` |
| `lastTxn` | 最近事务详情：`from, to, amount, state, votes`（yes / no / timeout）、`acked` |
| `phase` | 当前事务所处阶段（等待投票、等待决定、通知中、恢复中） |

参与者：

| 字段 | 含义 |
| --- | --- |
| `role` | `idle` / `prepared`（已投 YES，等结果）/ `uncertain`（已投 YES 且联系不上协调者） |
| `balance` | 账户余额 |
| `locked` | 锁住账户的事务 ID，空表示未锁 |
| `vote` | 投票策略 `auto` / `no` |
| `txns` | 本参与者见过的事务及结局 |

## 应用输入

| 方法 | 节点 | 作用 |
| --- | --- | --- |
| `Application.Transfer(from, to, amount, pauseBeforeDecisionMs, pauseAfterDecisionMs)` | node-1 | 发起转账。两个 pause 参数分别在「收齐投票后、写决定前」和「写决定后、通知前」停顿，留出崩溃协调者的窗口 |
| `Application.SetVote(policy)` | 参与者 | `no` 强制否决之后的 Prepare；`auto` 按余额校验 |

## 观察场景

每个场景都已作为一个实验创建（`node scripts/scenarios.mjs setup two-phase-commit`），也可用 `node scripts/scenarios.mjs run two-phase-commit <场景ID>` 自动回放后在「运行历史」中查看。

### commit · 转账成功提交（3 节点）

node-2 → node-3 转 30，再 node-3 → node-2 转 50。每个事务 2 条 Prepare + 2 条 Commit；余额 120 / 80，总额 200 守恒，三方对每个事务的结局一致。

思考：参与者为什么要在投 YES **之前**写日志？

### veto · 任一参与者否决即中止（3 节点）

1. node-2 转出 150：余额不足投 NO，node-3 投 YES 并加锁，但事务中止，node-3 收到 Abort 后释放锁。
2. node-3 设为强制否决，合法的 10 元转账同样中止。

思考：对比 Paxos/Raft 的多数派，为什么 2PC 必须等所有人？

### participant-crash · 参与者投票前崩溃（3 节点）

崩溃 node-3 后发起转账：node-2 已投 YES 并加锁，协调者等 node-3 投票 3 秒超时后中止，node-2 释放锁。node-3 恢复后没有 T1 的任何记录（它从未投票），下一笔转账正常提交。

思考：第 65 页「Why is it safe to abort?」——如果 node-3 是在投了 YES 之后才崩溃呢？

### coordinator-crash-blocks · 协调者在决定前崩溃（3 节点）

1. 转账带 `pauseBeforeDecisionMs=30000`：两个参与者都投了 YES，协调者在写决定前停顿，此时崩溃协调者。
2. 8 秒后两个参与者仍是 `uncertain`，`locked=T1`，反复发 `TM.Status` 却得不到回复——账户被锁，其他事务也无法使用。
3. 恢复协调者：日志里 T1 没有决定 → 中止，参与者释放锁。

思考：第 66 页「Can we vote abort here?」——两个参与者都投了 YES，它们能不能互相商量后自行提交？（提示：它们不知道协调者是否在崩溃前已经决定并告诉了别人。）

### coordinator-crash-after-commit · 协调者在提交点之后崩溃（3 节点）

1. 转账带 `pauseAfterDecisionMs=30000`：协调者已把 commit 写入日志，尚未通知就崩溃。
2. 事务在逻辑上已经提交，但参与者不知道，仍 `uncertain` 并持锁，余额未变。
3. 协调者恢复后读日志重发 Commit，余额变为 70 / 130。

思考：这里参与者若超时后自行中止会造成什么后果？第 67 页的「备用协调者」为什么又回到了共识问题？

## 实现边界

- 协调者日志与参与者状态（余额、锁、prepared 记录）都用 `sdk.Save` 持久化，崩溃恢复后继续；投票策略 `vote` 不持久化。
- 每个参与者只有一个账户、一把锁：已锁住时收到另一个事务的 Prepare 直接投 NO（不排队等待）。
- 只实现基本 2PC 和推定中止；没有协作式终结协议（参与者之间互相询问）、3PC 或备用协调者。协调者长期故障时参与者会一直阻塞，这正是 2PC 的缺陷。
- Abort 只发送一次；错过 Abort 的已 prepared 参与者靠询问协调者得知结果。
