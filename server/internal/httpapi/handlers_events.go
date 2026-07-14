package httpapi

import (
	"net/http"

	"wanxiang-agent/server/internal/events"
)

func handleEventStream(bus *events.Bus) http.HandlerFunc {
	return events.ServeSSE(bus)
}
