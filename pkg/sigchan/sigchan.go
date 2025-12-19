package sigchan

// Chan 是一个非阻塞的信号 channel
// 用于通知事件发生，但不传递数据
type Chan struct {
	c chan struct{}
}

// New 创建新的信号 channel
func New(bufferSize int) *Chan {
	return &Chan{
		c: make(chan struct{}, bufferSize),
	}
}

// Emit 发送信号（非阻塞）
func (c *Chan) Emit() {
	select {
	case c.c <- struct{}{}:
	default:
		// 如果 channel 已满，忽略（非阻塞）
	}
}

// C 返回内部的 channel（用于 select）
func (c *Chan) C() <-chan struct{} {
	return c.c
}

