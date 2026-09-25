# Berkeley 时钟同步

课件：L05《Distributed Synchronization (1)》第 22–26 页（Berkeley Algorithm）。

## 算法

- 内部同步：没有节点拥有 UTC。node-1 是 master（时间守护进程），只是协调者，它自己的时钟也可能不准。
- 每一轮（默认每 5s，或手动 `SyncRound`）：
  1. master 并行调用 `Berkeley.Poll` 询问所有节点的时钟，按 Cristian 的方法用 RTT/2 补偿，得到每个节点相对 master 的差 `diff`（master 自己为 0）；
  2. 求所有 diff 的中位数，`|diff − 中位数| > thresholdMs` 的节点视为离群值，不参与平均；
  3. 其余节点（含 master）的 diff 求平均 `avg`；
  4. 给每个节点（包括离群节点）发送**相对调整量** `Berkeley.Adjust{adjMs = avg − diff}`；master 自己调整 `avg`。
- 发送相对量而不是绝对时间：传输延迟不会让调整失准。
- 调整方式（第 26 页 “can't just change time”）：默认 **slew**，临时改变时钟速率（最多 ±80%，至少用 3 秒）逐渐追上，本地时间始终单调递增；`SetMode(step=true)` 改为直接跳变，快的时钟会被拨回去，`backwardJumps` 增加。

## 模拟时钟

每个节点模拟自己的硬件时钟：`local = anchorLocal + (now − anchorReal) × rate`，`rate = 1 + drift/1000`（slew 期间 rate 临时改变）；真实时间即宿主机时间。默认值：

| 节点 | node-1 (master) | node-2 | node-3 | node-4 | node-5 |
| --- | --- | --- | --- | --- | --- |
| 初始偏差 | +400ms | +1800ms | −1200ms | +2600ms | −700ms |
| 漂移率 | +8ms/s | +20ms/s | −15ms/s | +10ms/s | −25ms/s |

可用 `SetClock` 覆盖。时钟参数（含进行中的 slew）保存在 `/state`：节点崩溃期间硬件时钟照常走，恢复后接着读；其余状态（轮次、表格）只在内存中。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` | `master`（node-1）/ `client` |
| `localTime` / `errorMs` | 本地时钟读数 / 本地 − 真实时间（UTC 仅供观察，算法不知道） |
| `driftMsPerSec` | 漂移率 |
| `slewMsLeft` / `rate` | 正在 slew 时：剩余时间 / 当前时钟速率 |
| `backwardJumps` | 时钟被往回拨的次数 |
| `adjustments` / `lastAdjMs` | 收到的调整次数 / 最近一次调整量 |
| `sinceAdjustS` | 距最近一次调整的秒数 |
| `driftSinceAdjMs` | 最近一次调整完成后，因漂移又偏离了多少 |
| `rounds` / `periodMs` / `thresholdMs` / `mode` | master：轮次数 / 轮次间隔 / 离群阈值 / `slew` 或 `step` |
| `lastRound` | master：上一轮的表格，每个节点 `{diffMs, rttMs, used, adjMs}`；`used=false` 为离群值 |
| `avgMs` / `medianMs` | master：上一轮的平均值（只含非离群值）/ 中位数 |
| `spreadMs` / `spreadHistory` | master：上一轮测得的最大差距 / 最近 10 轮的最大差距 |
| `noReply` | master：上一轮没有回复的节点 |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.SyncRound()` | （master）立即执行一轮 |
| `Application.SetPeriod(periodMs)` | （master）轮次间隔，0 为只手动 |
| `Application.SetThreshold(thresholdMs)` | （master）离群阈值，默认 3000 |
| `Application.SetMode(step)` | （master）勾选为 step，否则 slew；随 Adjust 消息下发 |
| `Application.SetClock(offsetMs, driftMsPerSec)` | 重设本节点的时钟偏差和漂移率 |

## 观察场景

`node scripts/scenarios.mjs run clock-berkeley <场景ID>` 自动回放；也可以打开同名实验手动操作。

### converge · 周期轮次使时钟收敛（5 节点，延迟 100ms）

第一轮测得的最大差距约 3.8s；各节点 slew 几秒后，下一轮差距只剩下两轮之间漂移造成的一两百毫秒。但所有节点的 `errorMs` 都约为 +0.6s：平均值是五个错误时钟的平均，不是 UTC。整个过程没有时钟倒退。

思考：如果要同时与 UTC 对齐，应该怎么结合 NTP？

### outlier · 离群时钟不参与平均（5 节点）

node-4 快 10 秒。`lastRound.node-4.used = false`，平均值保持在几百毫秒以内（若计入 node-4 会被拉快约 1.7s），但 node-4 仍收到约 −9.9s 的调整量。之后几轮它被逐步拉回，差距小于阈值后重新参与平均。

思考：为什么用中位数而不是平均值来判断离群？如果有一半节点都坏了呢？

### step-vs-slew · 跳变 vs 平滑调整（4 节点）

先关闭周期轮次、手动两轮：slew 刚开始时差距仍有 3 秒以上，4 秒后再测才对齐，而且 `backwardJumps` 全为 0。再切换到 step，把 node-2 拨快到 +3s：一轮之后它立即对齐，但本地时间倒退了约 2 秒（`backwardJumps ≥ 1`）。

思考：`make` 这类依赖文件时间戳的程序，遇到时钟倒退会出什么问题？slew 的速率为什么要限制在一定范围内？

### master-crash · master 崩溃（4 节点）

收敛后崩溃 node-1。没有了轮次，node-2（+20ms/s）和 node-3（−15ms/s）朝相反方向漂移，20 秒后各自偏离 400ms / −300ms 左右（`driftSinceAdjMs`）。恢复 node-1 后，它的硬件时钟一直在走，内存状态从零开始，下一轮重新收敛。

思考：课件说可以选一个新的 master，但“not in bounded time”。用 L06 的 Bully 算法选主需要多少条消息？新 master 的时钟为什么不需要是“对的”？

## 实现边界

- master 固定为 node-1，不实现选举；轮询超时 2 秒，没有回复的节点记入 `noReply` 且本轮不调整。
- RTT/2 补偿受平台消息投递粒度（每 50ms 一次）影响，每个 diff 约有 ±25ms 的测量误差。
- 离群阈值是固定毫秒数，不根据 RTT 自适应；slew 至少 3 秒、速率变化不超过 80%，新一轮的 slew 会替代上一轮未完成的部分（新的调整量本就是按当前差距算的）。
