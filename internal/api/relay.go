package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	vmsflv "go-cam-server/internal/flv"
	"go-cam-server/internal/subscriber"
)

// GET /relay/{key}
//
// This is the Node A endpoint: streams the live FLV for {key} to Node B.
//
// Protocol:
//   - Response: Content-Type: video/x-flv, Transfer-Encoding: chunked
//   - Body: standard FLV format (header + tags) using internal/flv writer
//   - Connection stays open as long as the stream is live
//   - When Node B disconnects (request context cancelled), the relay subscriber
//     is automatically removed from the local stream
//
// Node B connects here via RelayManager.pull() using HTTP GET.
//
// Flow:
//
//	Node B  ─── GET /relay/cam1 ──────────────────► Node A
//	        ◄─── HTTP 200 chunked (FLV stream) ─────
//	                 │
//	     flv.Reader reads packets         flv.WriteTag writes packets
//	                 │                           ▲
//	     Node B StreamManager.Ingest()    RelaySubscriber.Deliver()
//	                                             ▲
//	                                      liveStream pump goroutine
func (s *Server) serveRelay(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	st, ok := s.manager.Get(key)
	if !ok {
		http.Error(w, "stream not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "video/x-flv")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Stream-Key", key)
	w.Header().Set("X-Node-ID", s.cfg.Node.ID)

	// Write the FLV file header so Node B's reader can initialise.
	if err := vmsflv.WriteFileHeader(w); err != nil {
		logrus.Errorf("relay-serve[%s]: write FLV header: %v", key, err)
		return
	}

	// Flush headers + FLV header to Node B immediately.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Create a RelaySubscriber and attach it to the local stream.
	relaySub := subscriber.NewRelaySubscriber(key, w)
	if err := st.AddSubscriber(relaySub); err != nil {
		logrus.Errorf("relay-serve[%s]: add subscriber: %v", key, err)
		return
	}
	defer st.RemoveSubscriber(relaySub.ID())

	logrus.Infof("relay-serve[%s]: node %s connected", key, r.RemoteAddr)

	// Block until the requesting node (Node B) disconnects or stream ends.
	<-r.Context().Done()
	logrus.Infof("relay-serve[%s]: node %s disconnected", key, r.RemoteAddr)
}
