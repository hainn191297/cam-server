package api

import (
	"encoding/json"
	"io"
	"net/http"

	"go-cam-server/internal/camera"
	"go-cam-server/internal/onvif"
)

// GET /onvif/discover
//
// Scans all network interfaces via WS-Discovery (ONVIF multicast) and returns
// the list of discovered device endpoints. Pass ?nic=eth0 to restrict to a
// specific network interface card.
func (s *Server) onvifDiscover(w http.ResponseWriter, r *http.Request) {
	nic := r.URL.Query().Get("nic") // optional; empty = scan all

	adapter := camera.NewONVIFAdapter()
	devices, err := adapter.Discover(nic)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"count":   len(devices),
		"devices": devices,
	})
}

// GET /onvif/info
//
// Fetches device information (manufacturer, model, serial number) from a
// specific ONVIF camera.
//
// Query params: endpoint, username, password
// Example: /onvif/info?endpoint=http://192.168.1.50:80/onvif/device_service&username=admin&password=xxx
func (s *Server) onvifGetInfo(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	endpoint := q.Get("endpoint")
	username := q.Get("username")
	password := q.Get("password")

	if endpoint == "" {
		http.Error(w, "endpoint query param is required", http.StatusBadRequest)
		return
	}

	info, err := onvif.GetDeviceInfo(endpoint, username, password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, info)
}

// POST /onvif/resolve
//
// Connects to an ONVIF device and returns a normalised CameraStream with RTSP
// stream URIs classified as main / sub.
//
// Request body (JSON):
//
//	{ "endpoint": "http://192.168.1.50:80/onvif/device_service", "username": "admin", "password": "pass" }
func (s *Server) onvifResolve(w http.ResponseWriter, r *http.Request) {
	var params camera.ResolveParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if params.Endpoint == "" {
		http.Error(w, "endpoint is required", http.StatusBadRequest)
		return
	}

	adapter := camera.NewONVIFAdapter()
	stream, err := adapter.Resolve(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, stream)
}

// GET /onvif/snapshot
func (s *Server) onvifSnapshot(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	endpoint := q.Get("endpoint")
	username := q.Get("username")
	password := q.Get("password")

	if endpoint == "" {
		http.Error(w, "endpoint is required", http.StatusBadRequest)
		return
	}

	snapURL, err := onvif.GetSnapshotURI(endpoint, username, password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Fetch the actual image from the camera
	resp, err := http.Get(snapURL)
	if err != nil {
		http.Error(w, "failed to download snapshot from camera: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "image/jpeg")
	io.Copy(w, resp.Body)
}

// POST /onvif/ptz
func (s *Server) onvifPTZ(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string  `json:"endpoint"`
		Username string  `json:"username"`
		Password string  `json:"password"`
		Pan      float64 `json:"pan"`
		Tilt     float64 `json:"tilt"`
		Zoom     float64 `json:"zoom"`
		Stop     bool    `json:"stop"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var err error
	if req.Stop {
		err = onvif.PTZStop(req.Endpoint, req.Username, req.Password)
	} else {
		err = onvif.PTZMove(req.Endpoint, req.Username, req.Password, req.Pan, req.Tilt, req.Zoom)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// POST /onvif/reboot
func (s *Server) onvifReboot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := onvif.Reboot(req.Endpoint, req.Username, req.Password); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "rebooting"})
}

// POST /camera/resolve
//
// Generic adapter endpoint. Accepts any adapter type (onvif, manual).
// This is the canonical entry-point — always use this in new code.
//
// Request body (JSON):
//
//	{ "source": "manual", "camera_id": "frontdoor",
//	  "manual_main": "rtsp://192.168.1.10/stream1",
//	  "manual_sub":  "rtsp://192.168.1.10/stream2" }
func (s *Server) cameraResolve(w http.ResponseWriter, r *http.Request) {
	var params camera.ResolveParams
	var sourceName string

	// Peek at "source" first to pick the right adapter.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if src, ok := raw["source"]; ok {
		_ = json.Unmarshal(src, &sourceName)
	}
	// Re-marshal into ResolveParams.
	b, _ := json.Marshal(raw)
	if err := json.Unmarshal(b, &params); err != nil {
		http.Error(w, "invalid params: "+err.Error(), http.StatusBadRequest)
		return
	}

	var adapter camera.Adapter
	switch sourceName {
	case "onvif":
		adapter = camera.NewONVIFAdapter()
	case "manual", "":
		adapter = camera.NewManualAdapter()
	default:
		http.Error(w, "unknown source adapter: "+sourceName, http.StatusBadRequest)
		return
	}

	stream, err := adapter.Resolve(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, stream)
}
