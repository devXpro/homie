// Command echo is the tiny HTTP backend of the relay docker smoke test
// (relay/test/run.sh). It answers every request with a JSON description of what
// it received, so the test can tell that a request travelled through
// Caddy -> frps -> frpc to the "home" side intact.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"time"
)

type reply struct {
	Marker string `json:"marker"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Host   string `json:"host"`
	Body   string `json:"body"`
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	marker := flag.String("marker", "homie-echo", "value returned in the marker field")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply{
			Marker: *marker,
			Method: r.Method,
			Path:   r.URL.RequestURI(),
			Host:   r.Host,
			Body:   string(body),
		})
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
