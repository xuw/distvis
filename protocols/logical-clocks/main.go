// distvis:name 逻辑时钟：Lamport 与向量时钟 · L05
// distvis:description 每个事件同时打上 Lamport 时间戳、全序 L.id 和向量时钟；比较两个事件，观察 L(e)<L(e') 不能推出 e→e'，而 V(e)<V(e') 当且仅当 e→e'。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	p, err := newProcess(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("Clock", p); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{p}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(p.run)
	return p.report()
}
