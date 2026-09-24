# 教学工具调研与选型

调研时间：2026-09-18。以下结论来自项目官方仓库说明；不是对所有同类产品的穷尽搜索。

| 项目 | 已确认的能力 | 对本需求的启发与差距 |
| --- | --- | --- |
| [Maelstrom](https://github.com/jepsen-io/maelstrom) | 学习分布式系统；任意语言的本地进程；JSON stdin/stdout 协议；模拟延迟、丢包、分区、进程故障；工作负载与一致性检查；时间线和 Lamport 图 | 最接近后端实验框架。采用“节点只写协议、消息经 coordinator 路由”的设计。其主要流程是 CLI 测试和结果分析，不直接提供本需求中的 Docker 节点管理与现代 H5 实验工作台 |
| [DSLabs](https://github.com/emichael/dslabs) | 消息驱动状态机、定时器、教学实验、模型检查、错误轨迹与图形化调试；Java 框架 | 参考可探索的执行轨迹和节点状态检查。语言及执行模型与 Go 容器不同；模型检查不是本版实现范围 |
| [RaftScope](https://github.com/ongardie/raftscope) | Raft 的浏览器可视化，README 明确提及 SVG 渲染与受 The Secret Lives of Data 启发 | 参考节点、消息与协议角色的可视化表达。针对 Raft，不是运行任意用户协议的容器平台 |
| [Chaos Mesh](https://github.com/chaos-mesh/chaos-mesh) | Kubernetes 原生故障注入平台；Pod、网络、I/O、时间等故障；工作流、调度与 Dashboard | 未来可接真实网络故障。在本版中采用 SDK 消息层注入，避免为课堂实验引入特权 DaemonSet，并准确关联每条消息和故障 |

直接查阅的材料：

- [Maelstrom README](https://github.com/jepsen-io/maelstrom/blob/main/README.md)，包括 Programming / Design Overview、CLI Options、License。
- [DSLabs README](https://github.com/emichael/dslabs/blob/master/README.md)，包括 Programming Model、Testing and Model Checking、Visualization。
- [RaftScope README](https://github.com/ongardie/raftscope/blob/master/README) 和仓库文件目录。
- [Chaos Mesh README](https://github.com/chaos-mesh/chaos-mesh/blob/master/README.md)，包括 Features、Architecture。

## 决策

已有工具值得参考，但在上述项目中没有确认到同时覆盖“用户 Go 程序、每节点独立容器、可控链路、浏览器逐步动画和实验历史”的现成组合。DistVis 因此独立实现统一事件格式、消息路由器和 H5 工作台，参考其设计思路，不拷贝实验答案或上游实现。

如果课程重点是严格验证学生协议的一致性，后续可增加 Maelstrom/Jepsen 检查器适配。如果重点是真实 TCP 网络行为，增加 Chaos Mesh 或 tc/netem 运行模式更合适。当前的 SDK 消息层故障与真实内核网络故障需要明确区分。
