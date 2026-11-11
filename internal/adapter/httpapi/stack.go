package httpapi

import "runtime"

// stackTrace returns the current goroutine's stack trace for logging when a
// panic is recovered. It is never sent with a response.
func stackTrace() string {
	const maxSize = 8 << 10 // 8KB is enough to find the cause without bloating the log
	buf := make([]byte, maxSize)
	n := runtime.Stack(buf, false)
	return string(buf[:n])
}
