// distvis:name Cristian 时钟同步 · L05
// distvis:description node-1 是持有 UTC 的时间服务器 S；客户端用自己的时钟测 RTT，把时钟设为 t + RTT/2，精度为 ±(RTT/2 − min)。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	c, err := newCristian(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("Time", c); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{c}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(c.run)
	return c.report()
}
