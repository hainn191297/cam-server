package camera

import "time"

const (
	DefaultConnectTimeoutMS = 5000
	DefaultReadTimeoutMS    = 15000
	DefaultProbeCadenceMS   = 30000
)

// Cam is the canonical camera aggregate used by the camera domain.
// Plaintext credentials intentionally do not live on this read model.
type Cam struct {
	CamID        string          `json:"cam_id"`
	TenantID     string          `json:"tenant_id"`
	Identity     CamIdentity     `json:"identity"`
	Site         CamSite         `json:"site"`
	Endpoint     CamEndpoint     `json:"endpoint"`
	Credentials  *CamCredentials `json:"credentials,omitempty"`
	Profiles     []CamProfile    `json:"profiles"`
	Capabilities CamCapabilities `json:"capabilities"`
	Health       CamHealth       `json:"health"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Version      int64           `json:"version"`
}

// CamWrite contains client-controlled fields accepted during create/update
// workflows. Server-managed fields such as IDs, health, timestamps, and
// version are intentionally absent.
type CamWrite struct {
	Identity     CamIdentity          `json:"identity"`
	Site         CamSite              `json:"site"`
	Endpoint     CamEndpoint          `json:"endpoint"`
	Credentials  *CamCredentialsWrite `json:"credentials,omitempty"`
	Profiles     []CamProfileWrite    `json:"profiles"`
	Capabilities CamCapabilities      `json:"capabilities"`
}

type CamIdentity struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Vendor       string `json:"vendor,omitempty"`
	Model        string `json:"model,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
}

type CamSite struct {
	SiteID  string            `json:"site_id"`
	GroupID string            `json:"group_id,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type EndpointProtocol string

const (
	EndpointProtocolRTSP EndpointProtocol = "rtsp"
)

type EndpointTransport string

const (
	EndpointTransportTCP  EndpointTransport = "tcp"
	EndpointTransportUDP  EndpointTransport = "udp"
	EndpointTransportAuto EndpointTransport = "auto"
)

type CamEndpoint struct {
	Protocol         EndpointProtocol  `json:"protocol"`
	URL              string            `json:"url"`
	Transport        EndpointTransport `json:"transport"`
	ConnectTimeoutMS int               `json:"connect_timeout_ms"`
	ReadTimeoutMS    int               `json:"read_timeout_ms"`
}

type CredentialScheme string

const (
	CredentialSchemeBasic  CredentialScheme = "basic"
	CredentialSchemeDigest CredentialScheme = "digest"
	CredentialSchemeNone   CredentialScheme = "none"
)

// CamCredentials is the read/storage-facing credential shape. It may expose an
// opaque secret reference, but it can never carry plaintext password material.
type CamCredentials struct {
	Scheme      CredentialScheme `json:"scheme"`
	Username    string           `json:"username,omitempty"`
	PasswordRef string           `json:"password_ref,omitempty"`
	RotatedAt   *time.Time       `json:"rotated_at,omitempty"`
}

// CamCredentialsWrite is the inbound mutation shape. Plaintext Password is
// accepted here only; it is not present on CamCredentials or Cam.
type CamCredentialsWrite struct {
	Scheme   CredentialScheme `json:"scheme"`
	Username string           `json:"username,omitempty"`
	Password string           `json:"password,omitempty"`
}

type CodecHint string

const (
	CodecHintH264    CodecHint = "h264"
	CodecHintH265    CodecHint = "h265"
	CodecHintMJPEG   CodecHint = "mjpeg"
	CodecHintUnknown CodecHint = "unknown"
)

type CamProfile struct {
	ProfileID       string          `json:"profile_id"`
	Name            string          `json:"name"`
	StreamPath      string          `json:"stream_path,omitempty"`
	CodecHint       CodecHint       `json:"codec_hint"`
	ResolutionHint  *ResolutionHint `json:"resolution_hint,omitempty"`
	BitrateKbpsHint *int            `json:"bitrate_kbps_hint,omitempty"`
	FramerateHint   *int            `json:"framerate_hint,omitempty"`
	IsDefault       bool            `json:"is_default"`
}

// CamProfileWrite omits the server-generated immutable profile ID while
// retaining the client-controlled profile fields.
type CamProfileWrite struct {
	Name            string          `json:"name"`
	StreamPath      string          `json:"stream_path,omitempty"`
	CodecHint       CodecHint       `json:"codec_hint"`
	ResolutionHint  *ResolutionHint `json:"resolution_hint,omitempty"`
	BitrateKbpsHint *int            `json:"bitrate_kbps_hint,omitempty"`
	FramerateHint   *int            `json:"framerate_hint,omitempty"`
	IsDefault       bool            `json:"is_default"`
}

type ResolutionHint struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type CamCapabilities struct {
	SupportsAudio    bool     `json:"supports_audio"`
	SupportsPTZ      bool     `json:"supports_ptz"`
	SupportsONVIF    bool     `json:"supports_onvif"`
	DiscoveredCodecs []string `json:"discovered_codecs,omitempty"`
	Notes            string   `json:"notes,omitempty"`
}

type HealthStatus string

const (
	HealthStatusUnknown  HealthStatus = "unknown"
	HealthStatusOnline   HealthStatus = "online"
	HealthStatusOffline  HealthStatus = "offline"
	HealthStatusDegraded HealthStatus = "degraded"
)

type CamHealth struct {
	Status              HealthStatus       `json:"status"`
	LastSeenAt          *time.Time         `json:"last_seen_at,omitempty"`
	LastError           *CamHealthError    `json:"last_error,omitempty"`
	Probe               HealthProbePointer `json:"probe"`
	ConsecutiveFailures int                `json:"consecutive_failures"`
}

type CamHealthError struct {
	Code       string    `json:"code"`
	Message    string    `json:"message"`
	ObservedAt time.Time `json:"observed_at"`
}

// HealthProbePointer identifies the health loop that owns the runtime health
// slice for a camera.
type HealthProbePointer struct {
	ProbeID     string     `json:"probe_id"`
	CadenceMS   int        `json:"cadence_ms"`
	LastRunAt   *time.Time `json:"last_run_at,omitempty"`
	NextRunAt   *time.Time `json:"next_run_at,omitempty"`
	OwnerNodeID string     `json:"owner_node_id"`
}
