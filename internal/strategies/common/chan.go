package common

// TrySend attempts a non-blocking send.
// Returns true if value was sent.
func TrySend[T any](ch chan<- T, v T) bool {
	select {
	case ch <- v:
		return true
	default:
		return false
	}
}

// TrySignal is a shorthand for non-blocking "signal" channels.
func TrySignal(ch chan<- struct{}) bool {
	return TrySend(ch, struct{}{})
}
