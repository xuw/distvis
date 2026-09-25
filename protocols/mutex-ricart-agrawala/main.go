// distvis:name Ricart & Agrawala 互斥 · L06
// distvis:description 基于 Lamport 全序时间戳的分布式互斥：请求广播给所有节点，时间戳更早的持有者推迟回复；每轮 2(n-1) 条消息。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	m, err := newMutex(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("RA", m); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{m}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(m.run)
	return m.report()
}
