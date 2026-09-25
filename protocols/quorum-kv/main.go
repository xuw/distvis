// distvis:name Quorum 复制 (N/W/R) · L07
// distvis:description 所有节点都是副本，任一节点可协调读写：写等 W 个确认、读等 R 个回复并取最高版本；W+R>N 时读写法定人数必然相交。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	q, err := newQuorum(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("Q", q); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{q}, lab.Application()); err != nil {
		return err
	}
	return q.report()
}
