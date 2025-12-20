package metrics

import (
	"expvar"
	"net/http"
	_ "net/http/pprof"
)

// Start 启动 metrics/debug 服务：
// - expvar: /debug/vars
// - pprof:  /debug/pprof
// 由调用方控制是否启用（建议仅监听 localhost 或内网）。
func Start(listenAddr string) error {
	mux := http.NewServeMux()
	mux.Handle("/debug/vars", expvar.Handler())
	// pprof 也注册在 DefaultServeMux 上
	mux.Handle("/debug/pprof/", http.DefaultServeMux)
	mux.Handle("/debug/pprof/cmdline", http.DefaultServeMux)
	mux.Handle("/debug/pprof/profile", http.DefaultServeMux)
	mux.Handle("/debug/pprof/symbol", http.DefaultServeMux)
	mux.Handle("/debug/pprof/trace", http.DefaultServeMux)

	s := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}
	return s.ListenAndServe()
}

