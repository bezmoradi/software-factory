package session

type waitEnd int

const (
	waitEmpty waitEnd = iota
	waitDelivered
	waitGone
	waitRevoked
)

type waiter struct {
	done     chan struct{}
	end      waitEnd
	messages []Fields
	answer   any
	closed   bool
}

func newWaiter() *waiter {
	return &waiter{done: make(chan struct{})}
}

func (w *waiter) finish(end waitEnd) {
	if w.closed {
		return
	}
	w.closed = true
	w.end = end
	close(w.done)
}
