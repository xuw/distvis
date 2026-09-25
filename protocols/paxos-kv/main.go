// distvis:name Paxos 复制状态机 KV · L07
// distvis:description kvpaxos：每个日志槽位是一个独立的 Paxos 实例，任意服务器接收 Put/Get，按槽位顺序执行，保证所有副本日志与存储一致；可观察并发竞争、崩溃恢复追赶与少数派分区。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	kv, err := newKV(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("Paxos", kv); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{kv}, lab.Application()); err != nil {
		return err
	}
	return kv.report()
}
