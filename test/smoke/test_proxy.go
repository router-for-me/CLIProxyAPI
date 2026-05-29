package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"time"
)

func main() {
	server := &http.Server{
		Addr: ":9091",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				log.Printf("[PROXY] CONNECT %s", r.Host)
				dest_conn, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
				if err != nil {
					http.Error(w, err.Error(), http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(http.StatusOK)
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					return
				}
				client_conn, _, err := hijacker.Hijack()
				if err != nil {
					return
				}
				go func() {
					defer dest_conn.Close()
					defer client_conn.Close()
					io.Copy(dest_conn, client_conn)
				}()
				go func() {
					defer dest_conn.Close()
					defer client_conn.Close()
					io.Copy(client_conn, dest_conn)
				}()
			} else {
				log.Printf("[PROXY] HTTP %s %s", r.Method, r.URL.String())
				http.Error(w, "HTTPS only", http.StatusBadRequest)
			}
		}),
	}
	log.Println("Starting proxy on :9091...")
	log.Fatal(server.ListenAndServe())
}