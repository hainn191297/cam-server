package api

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
)

type pionOfferRequest struct {
	Type  string `json:"type"`
	SDP   string `json:"sdp"`
	Offer *struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	} `json:"offer,omitempty"`
}

func (s *Server) pionOffer(w http.ResponseWriter, r *http.Request) {
	if s.pion == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "pion_webrtc is disabled",
		})
		return
	}

	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "stream key is required",
		})
		return
	}

	var req pionOfferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid json body",
		})
		return
	}

	typ := strings.TrimSpace(req.Type)
	sdp := strings.TrimSpace(req.SDP)
	if req.Offer != nil {
		if typ == "" {
			typ = strings.TrimSpace(req.Offer.Type)
		}
		if sdp == "" {
			sdp = strings.TrimSpace(req.Offer.SDP)
		}
	}
	if typ != "" && !strings.EqualFold(typ, "offer") {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "request type must be offer",
		})
		return
	}
	if sdp == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "offer sdp is required",
		})
		return
	}

	sessionID, answerSDP, err := s.pion.HandleOffer(r.Context(), key, sdp)
	if err != nil {
		logrus.WithError(err).WithField("stream_key", key).Warn("pion.offer.failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": err.Error(),
		})
		return
	}

	ttlSec := int(s.pion.SessionTTL().Seconds())
	writeJSON(w, http.StatusOK, map[string]any{
		"stream_key":   key,
		"session_id":   sessionID,
		"type":         "answer",
		"sdp":          answerSDP,
		"ttl_seconds":  ttlSec,
		"close_url":    "/pion/webrtc/session/" + sessionID,
		"playback_url": "/playback/" + key + "/timespans",
	})
}

func (s *Server) pionCloseSession(w http.ResponseWriter, r *http.Request) {
	if s.pion == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "pion_webrtc is disabled",
		})
		return
	}

	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "session id is required",
		})
		return
	}

	s.pion.CloseSession(id)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "closed",
		"session_id": id,
	})
}

func (s *Server) pionDemoPage(w http.ResponseWriter, r *http.Request) {
	if s.pion == nil {
		http.Error(w, "pion_webrtc is disabled", http.StatusServiceUnavailable)
		return
	}

	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" {
		http.Error(w, "stream key is required", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pionDemoTemplate.Execute(w, map[string]string{
		"StreamKey": key,
	})
}

var pionDemoTemplate = template.Must(template.New("pion-demo").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Pion WebRTC Demo</title>
  <style>
    :root { color-scheme: light; }
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 0; background: #0b1220; color: #e5ecf6; }
    .wrap { max-width: 960px; margin: 24px auto; padding: 0 16px; }
    video { width: 100%; background: #000; border-radius: 12px; }
    .row { margin: 12px 0; display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
    button { cursor: pointer; padding: 8px 14px; border-radius: 8px; border: 1px solid #5b9cff; background: #5b9cff; color: #fff; font-weight: 600; }
    button.secondary { background: transparent; }
    .log { margin-top: 12px; padding: 12px; border-radius: 8px; background: #121a2b; white-space: pre-wrap; min-height: 80px; }
    .meta { opacity: .8; font-size: 13px; }
    code { background: #121a2b; padding: 2px 6px; border-radius: 6px; }
  </style>
</head>
<body>
  <div class="wrap">
    <h2>Pion WebRTC Live Demo</h2>
    <p class="meta">Stream: <code id="stream">{{ .StreamKey }}</code></p>
    <video id="video" autoplay playsinline controls muted></video>
    <div class="row">
      <button id="start">Start</button>
      <button id="stop" class="secondary">Stop</button>
      <span class="meta">Offer endpoint: <code id="endpoint"></code></span>
    </div>
    <div id="log" class="log"></div>
  </div>

  <script>
    const streamKey = document.getElementById('stream').textContent.trim();
    const offerPath = '/pion/webrtc/' + encodeURIComponent(streamKey) + '/offer';
    document.getElementById('endpoint').textContent = offerPath;

    const logEl = document.getElementById('log');
    const videoEl = document.getElementById('video');
    const startBtn = document.getElementById('start');
    const stopBtn = document.getElementById('stop');

    let pc = null;
    let sessionID = '';

    const log = (...args) => {
      logEl.textContent += args.join(' ') + '\n';
      logEl.scrollTop = logEl.scrollHeight;
    };

    const waitIce = (peer) => new Promise(resolve => {
      if (peer.iceGatheringState === 'complete') return resolve();
      const onChange = () => {
        if (peer.iceGatheringState === 'complete') {
          peer.removeEventListener('icegatheringstatechange', onChange);
          resolve();
        }
      };
      peer.addEventListener('icegatheringstatechange', onChange);
    });

    async function closeSession() {
      if (sessionID) {
        try {
          await fetch('/pion/webrtc/session/' + encodeURIComponent(sessionID), { method: 'DELETE', keepalive: true });
        } catch (_) {}
      }
      sessionID = '';
    }

    async function stopPlayback() {
      if (pc) {
        try { pc.close(); } catch (_) {}
      }
      pc = null;
      await closeSession();
      log('stopped');
    }

    async function startPlayback() {
      await stopPlayback();

      pc = new RTCPeerConnection();
      pc.addTransceiver('video', { direction: 'recvonly' });
      pc.addTransceiver('audio', { direction: 'recvonly' });

      pc.ontrack = (ev) => {
        if (ev.streams && ev.streams[0]) {
          videoEl.srcObject = ev.streams[0];
        }
      };
      pc.onconnectionstatechange = () => log('pc state:', pc.connectionState);
      pc.oniceconnectionstatechange = () => log('ice state:', pc.iceConnectionState);

      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      await waitIce(pc);

      const resp = await fetch(offerPath, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          type: pc.localDescription.type,
          sdp: pc.localDescription.sdp
        })
      });
      if (!resp.ok) {
        const text = await resp.text();
        throw new Error('offer failed: ' + resp.status + ' ' + text);
      }

      const data = await resp.json();
      sessionID = data.session_id || '';
      if (!data.sdp) {
        throw new Error('missing answer sdp');
      }

      await pc.setRemoteDescription({ type: 'answer', sdp: data.sdp });
      log('connected, session=' + sessionID + ', ttl=' + String(data.ttl_seconds || '') + 's');
    }

    startBtn.onclick = () => {
      startPlayback().catch(err => log('error:', err.message || String(err)));
    };
    stopBtn.onclick = () => {
      stopPlayback().catch(err => log('error:', err.message || String(err)));
    };
    window.addEventListener('beforeunload', () => { closeSession(); });
  </script>
</body>
</html>
`))
