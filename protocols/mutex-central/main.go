// distvis:name 集中式互斥 · L06
// distvis:description 协调者 node-1 用 FIFO 队列分配锁：Request / Grant / Release 每轮 3 条消息；可观察协调者崩溃、重启失忆与持久化。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	c, err := newCentral(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("Coord", &Coord{c}); err != nil {
		return err
	}
	if err := node.RegisterName("Client", &Client{c}); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{c}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(c.run)
	return c.report()
}
