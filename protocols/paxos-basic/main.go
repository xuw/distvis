// distvis:name Basic Paxos 单值共识 · L07
// distvis:description 单值 Basic Paxos：任意节点以提案号 round.node 发起 Prepare/Accept 两阶段，多数派接受即选定；可复现课件四种情形、活锁与“重启忘记投票”导致的不安全。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	p, err := newPaxos(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("Paxos", p); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{p}, lab.Application()); err != nil {
		return err
	}
	return p.report()
}
