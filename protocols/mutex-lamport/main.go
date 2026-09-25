// distvis:name Lamport 互斥 · L06
// distvis:description 每个节点维护按 (L, id) 排序的请求优先队列（复制的数据结构）：请求广播、单播回复、退出时广播释放，每次进入 3(n-1) 条消息，依赖 FIFO 通道。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	m, err := newMutex(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("Lamport", m); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{m}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(m.run)
	return m.report()
}
