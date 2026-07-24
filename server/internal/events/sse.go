package events

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ServeSSE 将事件订阅转换为 SSE 响应。
func ServeSSE(bus *Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		ch, unsubscribe := bus.Subscribe()
		defer unsubscribe()
		flusher, _ := w.(http.Flusher)
		for {
			select {
			case <-r.Context().Done():
				return
			case event := <-ch:
				body, _ := json.Marshal(event)
				fmt.Fprintf(w, "event: %s\n", event.Type)
				fmt.Fprintf(w, "data: %s\n\n", body)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}
}
