package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleBotLogsTail(w http.ResponseWriter, r *http.Request) {
	botID := chiURLParam(r, "botID")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	b, err := s.getBot(ctx, botID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get: %v", err))
		return
	}
	if b == nil {
		writeError(w, 404, "bot not found")
		return
	}

	tailN := 200
	if v := strings.TrimSpace(r.URL.Query().Get("tail")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
			tailN = n
		}
	}

	lines, err := tailLines(b.LogPath, tailN, 256*1024)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("read log: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{"bot_id": botID, "lines": lines})
}

func (s *Server) handleBotLogsStream(w http.ResponseWriter, r *http.Request) {
	botID := chiURLParam(r, "botID")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	b, err := s.getBot(ctx, botID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get: %v", err))
		return
	}
	if b == nil {
		writeError(w, 404, "bot not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")

	// 打开日志文件并 seek 到末尾，后续轮询读取追加内容
	f, err := os.Open(b.LogPath)
	if err != nil {
		// 日志文件可能尚未创建
		fmt.Fprintf(w, "event: info\ndata: log file not found yet\n\n")
		flusher.Flush()
		<-r.Context().Done()
		return
	}
	defer f.Close()

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		writeError(w, 500, fmt.Sprintf("seek log: %v", err))
		return
	}

	notify := r.Context().Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	buf := make([]byte, 32*1024)
	var partial strings.Builder

	sendLine := func(line string) {
		// SSE 一行一个 data，避免长行把前端卡死
		line = strings.TrimRight(line, "\r\n")
		fmt.Fprintf(w, "data: %s\n\n", escapeSSE(line))
		flusher.Flush()
	}

	for {
		select {
		case <-notify:
			return
		case <-keepAlive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-ticker.C:
			n, err := f.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				partial.WriteString(chunk)

				// 以 '\n' 切行
				for {
					s := partial.String()
					idx := strings.IndexByte(s, '\n')
					if idx < 0 {
						break
					}
					line := s[:idx]
					sendLine(line)

					// 重置 builder：保留剩余部分
					rest := s[idx+1:]
					partial.Reset()
					partial.WriteString(rest)
				}
			}
			if err != nil && err != io.EOF {
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", escapeSSE(err.Error()))
				flusher.Flush()
				return
			}
		}
	}
}

// tailLines: 从文件末尾最多读取 maxBytes，取最后 n 行。
func tailLines(path string, n int, maxBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size <= 0 {
		return []string{}, nil
	}

	start := int64(0)
	if size > maxBytes {
		start = size - maxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}

	r := bufio.NewReader(f)
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			lines = append(lines, strings.TrimRight(line, "\r\n"))
			if len(lines) > n {
				// 只保留最后 n 行（滑动窗口）
				lines = lines[len(lines)-n:]
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}
	return lines, nil
}

func escapeSSE(s string) string {
	// 防止注入多行事件：把 CR/LF 变成可见符号
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}
