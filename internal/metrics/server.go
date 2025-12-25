package metrics

import (
	"context"
	"errors"
	"expvar"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/debug/vars", expvar.Handler())

	// pprof：显式注册到我们的 mux，避免依赖 DefaultServeMux 的全局副作用
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// Start 启动 metrics/debug 服务：
// - expvar: /debug/vars
// - pprof:  /debug/pprof
// 由调用方控制是否启用（建议仅监听 localhost 或内网）。
func Start(listenAddr string) error {
	s := &http.Server{
		Addr:    listenAddr,
		Handler: newMux(),
	}
	return s.ListenAndServe()
}

// StartAsync 启动 metrics/debug 服务（非阻塞），并在 ctx.Done() 时优雅关闭。
// 返回启动中的 server，便于调用方做额外管理/观测。
func StartAsync(ctx context.Context, listenAddr string) (*http.Server, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	s := &http.Server{
		Addr:    listenAddr,
		Handler: newMux(),
	}

	go func() {
		if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// 这里不记录日志：由调用方在需要时自行记录（避免引入 logger 依赖）
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(shutdownCtx)
	}()

	return s, nil
}
