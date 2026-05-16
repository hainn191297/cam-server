package camera

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

var labelKeyPattern = regexp.MustCompile(`^[a-z0-9._-]{1,63}$`)

func (c Cam) Validate() error {
	var errs []error

	errs = append(errs,
		validateUUIDV7("cam_id", c.CamID),
		validateUUIDV7("tenant_id", c.TenantID),
		c.Identity.Validate(),
		c.Site.Validate(),
		c.Endpoint.Validate(),
		validateProfiles(c.Profiles),
		c.Capabilities.Validate(),
		c.Health.Validate(),
	)

	if c.Credentials != nil {
		errs = append(errs, c.Credentials.Validate())
	}
	if c.CreatedAt.IsZero() {
		errs = append(errs, errors.New("created_at is required"))
	}
	if c.UpdatedAt.IsZero() {
		errs = append(errs, errors.New("updated_at is required"))
	}
	if !c.CreatedAt.IsZero() && !c.UpdatedAt.IsZero() && c.UpdatedAt.Before(c.CreatedAt) {
		errs = append(errs, errors.New("updated_at must not be before created_at"))
	}
	if c.Version < 0 {
		errs = append(errs, errors.New("version must be non-negative"))
	}

	return errors.Join(errs...)
}

func (c CamWrite) Validate() error {
	var errs []error

	errs = append(errs,
		c.Identity.Validate(),
		c.Site.Validate(),
		c.Endpoint.Validate(),
		validateWriteProfiles(c.Profiles),
		c.Capabilities.Validate(),
	)
	if c.Credentials != nil {
		errs = append(errs, c.Credentials.Validate())
	}

	return errors.Join(errs...)
}

func (i CamIdentity) Validate() error {
	var errs []error

	errs = append(errs, validateTrimmedString("identity.name", i.Name, 1, 120))
	if runeCount(i.Description) > 2000 {
		errs = append(errs, errors.New("identity.description must be at most 2000 characters"))
	}

	return errors.Join(errs...)
}

func (s CamSite) Validate() error {
	var errs []error

	errs = append(errs, validateUUIDV7("site.site_id", s.SiteID))
	if s.GroupID != "" {
		errs = append(errs, validateUUIDV7("site.group_id", s.GroupID))
	}
	for key, value := range s.Labels {
		if !labelKeyPattern.MatchString(key) {
			errs = append(errs, fmt.Errorf("site.labels key %q is invalid", key))
		}
		if runeCount(value) > 256 {
			errs = append(errs, fmt.Errorf("site.labels[%q] must be at most 256 characters", key))
		}
	}

	return errors.Join(errs...)
}

func (p EndpointProtocol) Valid() bool {
	return p == EndpointProtocolRTSP
}

func (t EndpointTransport) Valid() bool {
	switch t {
	case EndpointTransportTCP, EndpointTransportUDP, EndpointTransportAuto:
		return true
	default:
		return false
	}
}

func (e CamEndpoint) Validate() error {
	var errs []error

	if !e.Protocol.Valid() {
		errs = append(errs, fmt.Errorf("endpoint.protocol %q is invalid", e.Protocol))
	}
	parsed, err := url.ParseRequestURI(e.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		errs = append(errs, errors.New("endpoint.url must be an absolute URI"))
	} else {
		if EndpointProtocol(parsed.Scheme) != e.Protocol {
			errs = append(errs, errors.New("endpoint.url scheme must equal endpoint.protocol"))
		}
		if parsed.User != nil {
			errs = append(errs, errors.New("endpoint.url must not include userinfo"))
		}
	}
	if !e.Transport.Valid() {
		errs = append(errs, fmt.Errorf("endpoint.transport %q is invalid", e.Transport))
	}
	if e.ConnectTimeoutMS < 1000 || e.ConnectTimeoutMS > 30000 {
		errs = append(errs, errors.New("endpoint.connect_timeout_ms must be between 1000 and 30000"))
	}
	if e.ReadTimeoutMS < 1000 || e.ReadTimeoutMS > 60000 {
		errs = append(errs, errors.New("endpoint.read_timeout_ms must be between 1000 and 60000"))
	}

	return errors.Join(errs...)
}

func (s CredentialScheme) Valid() bool {
	switch s {
	case CredentialSchemeBasic, CredentialSchemeDigest, CredentialSchemeNone:
		return true
	default:
		return false
	}
}

func (c CamCredentials) Validate() error {
	var errs []error

	if !c.Scheme.Valid() {
		errs = append(errs, fmt.Errorf("credentials.scheme %q is invalid", c.Scheme))
		return errors.Join(errs...)
	}
	if c.Scheme == CredentialSchemeNone {
		if c.Username != "" {
			errs = append(errs, errors.New("credentials.username must be absent when credentials.scheme is none"))
		}
		if c.PasswordRef != "" {
			errs = append(errs, errors.New("credentials.password_ref must be absent when credentials.scheme is none"))
		}
		return errors.Join(errs...)
	}

	errs = append(errs, validateTrimmedString("credentials.username", c.Username, 1, 128))
	if c.PasswordRef == "" {
		errs = append(errs, errors.New("credentials.password_ref is required when credentials.scheme is not none"))
	}

	return errors.Join(errs...)
}

func (c CamCredentialsWrite) Validate() error {
	var errs []error

	if !c.Scheme.Valid() {
		errs = append(errs, fmt.Errorf("credentials.scheme %q is invalid", c.Scheme))
		return errors.Join(errs...)
	}
	if c.Scheme == CredentialSchemeNone {
		if c.Username != "" {
			errs = append(errs, errors.New("credentials.username must be absent when credentials.scheme is none"))
		}
		if c.Password != "" {
			errs = append(errs, errors.New("credentials.password must be absent when credentials.scheme is none"))
		}
		return errors.Join(errs...)
	}

	errs = append(errs, validateTrimmedString("credentials.username", c.Username, 1, 128))
	if c.Password == "" {
		errs = append(errs, errors.New("credentials.password is required when credentials.scheme is not none"))
	}

	return errors.Join(errs...)
}

func (c CodecHint) Valid() bool {
	switch c {
	case CodecHintH264, CodecHintH265, CodecHintMJPEG, CodecHintUnknown:
		return true
	default:
		return false
	}
}

func (p CamProfile) Validate() error {
	var errs []error

	errs = append(errs, validateUUIDV7("profile.profile_id", p.ProfileID))
	errs = append(errs, validateProfileFields(
		p.Name,
		p.StreamPath,
		p.CodecHint,
		p.ResolutionHint,
		p.BitrateKbpsHint,
		p.FramerateHint,
	))

	return errors.Join(errs...)
}

func (p CamProfileWrite) Validate() error {
	return validateProfileFields(
		p.Name,
		p.StreamPath,
		p.CodecHint,
		p.ResolutionHint,
		p.BitrateKbpsHint,
		p.FramerateHint,
	)
}

func (r ResolutionHint) Validate() error {
	var errs []error

	if r.Width < 1 || r.Width > 8192 {
		errs = append(errs, errors.New("profile.resolution_hint.width must be between 1 and 8192"))
	}
	if r.Height < 1 || r.Height > 8192 {
		errs = append(errs, errors.New("profile.resolution_hint.height must be between 1 and 8192"))
	}

	return errors.Join(errs...)
}

func (c CamCapabilities) Validate() error {
	if runeCount(c.Notes) > 2000 {
		return errors.New("capabilities.notes must be at most 2000 characters")
	}
	return nil
}

func (s HealthStatus) Valid() bool {
	switch s {
	case HealthStatusUnknown, HealthStatusOnline, HealthStatusOffline, HealthStatusDegraded:
		return true
	default:
		return false
	}
}

func (h CamHealth) Validate() error {
	var errs []error

	if !h.Status.Valid() {
		errs = append(errs, fmt.Errorf("health.status %q is invalid", h.Status))
	}
	if h.LastError != nil {
		errs = append(errs, h.LastError.Validate())
	}
	if (h.Status == HealthStatusOffline || h.Status == HealthStatusDegraded) && h.LastError == nil {
		errs = append(errs, errors.New("health.last_error is required when health.status is offline or degraded"))
	}
	errs = append(errs, h.Probe.Validate())
	if h.ConsecutiveFailures < 0 {
		errs = append(errs, errors.New("health.consecutive_failures must be non-negative"))
	}

	return errors.Join(errs...)
}

func (e CamHealthError) Validate() error {
	var errs []error

	errs = append(errs,
		validateTrimmedString("health.last_error.code", e.Code, 1, 0),
		validateTrimmedString("health.last_error.message", e.Message, 1, 0),
	)
	if e.ObservedAt.IsZero() {
		errs = append(errs, errors.New("health.last_error.observed_at is required"))
	}

	return errors.Join(errs...)
}

func (p HealthProbePointer) Validate() error {
	var errs []error

	errs = append(errs, validateUUIDV7("health.probe.probe_id", p.ProbeID))
	if p.CadenceMS < 5000 || p.CadenceMS > 600000 {
		errs = append(errs, errors.New("health.probe.cadence_ms must be between 5000 and 600000"))
	}
	errs = append(errs, validateTrimmedString("health.probe.owner_node_id", p.OwnerNodeID, 1, 0))

	return errors.Join(errs...)
}

func validateProfiles(profiles []CamProfile) error {
	if len(profiles) == 0 {
		return errors.New("profiles must contain at least one profile")
	}

	var errs []error
	seenNames := make(map[string]struct{}, len(profiles))
	defaultCount := 0

	for _, profile := range profiles {
		errs = append(errs, profile.Validate())
		if _, exists := seenNames[profile.Name]; exists {
			errs = append(errs, fmt.Errorf("profile.name %q must be unique within the camera", profile.Name))
		}
		seenNames[profile.Name] = struct{}{}
		if profile.IsDefault {
			defaultCount++
		}
	}
	if defaultCount != 1 {
		errs = append(errs, errors.New("profiles must contain exactly one default profile"))
	}

	return errors.Join(errs...)
}

func validateWriteProfiles(profiles []CamProfileWrite) error {
	if len(profiles) == 0 {
		return errors.New("profiles must contain at least one profile")
	}

	var errs []error
	seenNames := make(map[string]struct{}, len(profiles))
	defaultCount := 0

	for _, profile := range profiles {
		errs = append(errs, profile.Validate())
		if _, exists := seenNames[profile.Name]; exists {
			errs = append(errs, fmt.Errorf("profile.name %q must be unique within the camera", profile.Name))
		}
		seenNames[profile.Name] = struct{}{}
		if profile.IsDefault {
			defaultCount++
		}
	}
	if defaultCount != 1 {
		errs = append(errs, errors.New("profiles must contain exactly one default profile"))
	}

	return errors.Join(errs...)
}

func validateProfileFields(
	name string,
	streamPath string,
	codecHint CodecHint,
	resolutionHint *ResolutionHint,
	bitrateKbpsHint *int,
	framerateHint *int,
) error {
	var errs []error

	errs = append(errs, validateTrimmedString("profile.name", name, 1, 64))
	if streamPath != "" && !strings.HasPrefix(streamPath, "/") {
		errs = append(errs, errors.New("profile.stream_path must begin with / when present"))
	}
	if !codecHint.Valid() {
		errs = append(errs, fmt.Errorf("profile.codec_hint %q is invalid", codecHint))
	}
	if resolutionHint != nil {
		errs = append(errs, resolutionHint.Validate())
	}
	if bitrateKbpsHint != nil && (*bitrateKbpsHint < 1 || *bitrateKbpsHint > 200000) {
		errs = append(errs, errors.New("profile.bitrate_kbps_hint must be between 1 and 200000"))
	}
	if framerateHint != nil && (*framerateHint < 1 || *framerateHint > 240) {
		errs = append(errs, errors.New("profile.framerate_hint must be between 1 and 240"))
	}

	return errors.Join(errs...)
}

func validateUUIDV7(field, value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.Version() != 7 || parsed.String() != value {
		return fmt.Errorf("%s must be a canonical lower-case UUID v7", field)
	}
	return nil
}

func validateTrimmedString(field, value string, minRunes, maxRunes int) error {
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be trimmed", field)
	}
	length := runeCount(value)
	if length < minRunes {
		return fmt.Errorf("%s must be at least %d characters", field, minRunes)
	}
	if maxRunes > 0 && length > maxRunes {
		return fmt.Errorf("%s must be at most %d characters", field, maxRunes)
	}
	return nil
}

func runeCount(value string) int {
	return utf8.RuneCountInString(value)
}
