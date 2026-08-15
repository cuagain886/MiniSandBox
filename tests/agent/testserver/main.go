// Package main 提供端口代理验收使用的最小 sandbox 内 HTTP 服务。
// 它只在 loopback 监听固定端口，响应 GET /hello 与 POST /echo。
package main

import (
	"io"
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello-from-sandbox")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_, _ = w.Write(append(body, '!'))
	})
	server := &http.Server{Addr: "127.0.0.1:18080", Handler: mux}
	log.Fatal(server.ListenAndServe())
}
