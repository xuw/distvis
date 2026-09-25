// distvis:name 主备复制与一致性模型 · L07
// distvis:description node-1 为主副本，写入经主副本编号后复制到备份；可切换同步/异步复制，并比较本地读、主副本读与会话读（读己之写）的一致性。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	p, err := newReplica(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("PB", p); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{p}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(p.run)
	return p.report()
}
