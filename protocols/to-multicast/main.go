// distvis:name 全序多播：复制银行账户 · L06
// distvis:description 课件的银行例子：存款与计息按发送方 Lamport 时间戳进入优先队列，所有节点 ACK 后才按队首交付，所有副本以相同顺序更新；切换到 naive 模式可见副本分歧 1111 与 1110。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	r, err := newReplica(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("TOM", r); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{r}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(r.run)
	return r.report()
}
