package camera

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCamValidateAcceptsCanonicalShape(t *testing.T) {
	t.Parallel()

	if err := validCam().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestCamValidateRejectsCrossFieldViolations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Cam)
		want   string
	}{
		{
			name: "endpoint scheme mismatch",
			mutate: func(cam *Cam) {
				cam.Endpoint.URL = "http://10.20.30.40:554/Streaming/Channels/101"
			},
			want: "endpoint.url scheme must equal endpoint.protocol",
		},
		{
			name: "endpoint userinfo",
			mutate: func(cam *Cam) {
				cam.Endpoint.URL = "rtsp://viewer:secret@10.20.30.40:554/Streaming/Channels/101"
			},
			want: "endpoint.url must not include userinfo",
		},
		{
			name: "duplicate profile names",
			mutate: func(cam *Cam) {
				cam.Profiles[1].Name = "main"
			},
			want: `profile.name "main" must be unique within the camera`,
		},
		{
			name: "missing default profile",
			mutate: func(cam *Cam) {
				cam.Profiles[0].IsDefault = false
			},
			want: "profiles must contain exactly one default profile",
		},
		{
			name: "non canonical UUID",
			mutate: func(cam *Cam) {
				cam.CamID = strings.ToUpper(cam.CamID)
			},
			want: "cam_id must be a canonical lower-case UUID v7",
		},
		{
			name: "credentials require username",
			mutate: func(cam *Cam) {
				cam.Credentials.Username = ""
			},
			want: "credentials.username must be at least 1 characters",
		},
		{
			name: "offline health requires error",
			mutate: func(cam *Cam) {
				cam.Health.Status = HealthStatusOffline
				cam.Health.LastError = nil
			},
			want: "health.last_error is required when health.status is offline or degraded",
		},
		{
			name: "invalid label key",
			mutate: func(cam *Cam) {
				cam.Site.Labels["Zone"] = "lobby"
			},
			want: `site.labels key "Zone" is invalid`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cam := validCam()
			tt.mutate(&cam)

			err := cam.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCredentialReadWriteSeparation(t *testing.T) {
	t.Parallel()

	camJSON, err := json.Marshal(validCam())
	if err != nil {
		t.Fatalf("json.Marshal(Cam) error = %v", err)
	}
	if strings.Contains(string(camJSON), `"password"`) {
		t.Fatalf("Cam JSON unexpectedly contains plaintext password: %s", camJSON)
	}
	if !strings.Contains(string(camJSON), `"password_ref"`) {
		t.Fatalf("Cam JSON should contain the opaque password reference: %s", camJSON)
	}

	write := validCamWrite()
	if err := write.Validate(); err != nil {
		t.Fatalf("CamWrite.Validate() error = %v", err)
	}
	writeJSON, err := json.Marshal(write)
	if err != nil {
		t.Fatalf("json.Marshal(CamWrite) error = %v", err)
	}
	if !strings.Contains(string(writeJSON), `"password":"secret"`) {
		t.Fatalf("CamWrite JSON should contain inbound plaintext password: %s", writeJSON)
	}
	if strings.Contains(string(writeJSON), `"password_ref"`) {
		t.Fatalf("CamWrite JSON unexpectedly contains password_ref: %s", writeJSON)
	}
	if strings.Contains(string(writeJSON), `"profile_id"`) {
		t.Fatalf("CamWrite JSON unexpectedly contains server-generated profile_id: %s", writeJSON)
	}
}

func TestHealthStatusValidity(t *testing.T) {
	t.Parallel()

	for _, status := range []HealthStatus{
		HealthStatusUnknown,
		HealthStatusOnline,
		HealthStatusOffline,
		HealthStatusDegraded,
	} {
		if !status.Valid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if HealthStatus("recovering").Valid() {
		t.Fatal(`status "recovering" should be invalid`)
	}
}

func validCam() Cam {
	now := time.Date(2026, 5, 15, 9, 42, 11, 0, time.UTC)
	rotatedAt := time.Date(2026, 5, 10, 8, 14, 0, 0, time.UTC)
	lastRunAt := now
	nextRunAt := now.Add(30 * time.Second)
	bitrateMain := 4096
	framerateMain := 25

	return Cam{
		CamID:    "018f1c54-7c8a-7a1e-9b62-4f6f31c1a9e2",
		TenantID: "018f1c54-1111-7000-8000-000000000001",
		Identity: CamIdentity{
			Name:         "Front Door",
			Description:  "Entrance camera, lobby side",
			Vendor:       "hikvision",
			Model:        "DS-2CD2042WD-I",
			SerialNumber: "DS2042-XYZ-001",
		},
		Site: CamSite{
			SiteID:  "018f1c54-2222-7000-8000-000000000010",
			GroupID: "018f1c54-2222-7000-8000-000000000011",
			Labels: map[string]string{
				"zone":        "lobby",
				"criticality": "high",
			},
		},
		Endpoint: CamEndpoint{
			Protocol:         EndpointProtocolRTSP,
			URL:              "rtsp://10.20.30.40:554/Streaming/Channels/101",
			Transport:        EndpointTransportTCP,
			ConnectTimeoutMS: DefaultConnectTimeoutMS,
			ReadTimeoutMS:    DefaultReadTimeoutMS,
		},
		Credentials: &CamCredentials{
			Scheme:      CredentialSchemeDigest,
			Username:    "viewer",
			PasswordRef: "secret://local/cams/018f1c54-7c8a-7a1e-9b62-4f6f31c1a9e2/password",
			RotatedAt:   &rotatedAt,
		},
		Profiles: []CamProfile{
			{
				ProfileID:       "018f1c54-7c8a-7b00-8000-000000000001",
				Name:            "main",
				StreamPath:      "/Streaming/Channels/101",
				CodecHint:       CodecHintH264,
				ResolutionHint:  &ResolutionHint{Width: 1920, Height: 1080},
				BitrateKbpsHint: &bitrateMain,
				FramerateHint:   &framerateMain,
				IsDefault:       true,
			},
			{
				ProfileID:  "018f1c54-7c8a-7b00-8000-000000000002",
				Name:       "sub",
				StreamPath: "/Streaming/Channels/102",
				CodecHint:  CodecHintH264,
			},
		},
		Capabilities: CamCapabilities{
			SupportsONVIF:    true,
			DiscoveredCodecs: []string{"h264"},
		},
		Health: CamHealth{
			Status:     HealthStatusOnline,
			LastSeenAt: &now,
			Probe: HealthProbePointer{
				ProbeID:     "018f1c54-7c8a-7c00-8000-000000000001",
				CadenceMS:   DefaultProbeCadenceMS,
				LastRunAt:   &lastRunAt,
				NextRunAt:   &nextRunAt,
				OwnerNodeID: "node-01",
			},
		},
		CreatedAt: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: now,
		Version:   7,
	}
}

func validCamWrite() CamWrite {
	cam := validCam()
	return CamWrite{
		Identity: cam.Identity,
		Site:     cam.Site,
		Endpoint: cam.Endpoint,
		Credentials: &CamCredentialsWrite{
			Scheme:   CredentialSchemeDigest,
			Username: "viewer",
			Password: "secret",
		},
		Profiles: []CamProfileWrite{
			{
				Name:            cam.Profiles[0].Name,
				StreamPath:      cam.Profiles[0].StreamPath,
				CodecHint:       cam.Profiles[0].CodecHint,
				ResolutionHint:  cam.Profiles[0].ResolutionHint,
				BitrateKbpsHint: cam.Profiles[0].BitrateKbpsHint,
				FramerateHint:   cam.Profiles[0].FramerateHint,
				IsDefault:       cam.Profiles[0].IsDefault,
			},
			{
				Name:       cam.Profiles[1].Name,
				StreamPath: cam.Profiles[1].StreamPath,
				CodecHint:  cam.Profiles[1].CodecHint,
				IsDefault:  cam.Profiles[1].IsDefault,
			},
		},
		Capabilities: cam.Capabilities,
	}
}
