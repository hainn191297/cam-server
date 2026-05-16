package camera

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrCamNotFound = errors.New("camera not found")
)

type ErrCamValidation struct {
	Err error
}

func (e ErrCamValidation) Error() string {
	return fmt.Sprintf("camera validation failed: %v", e.Err)
}

func (e ErrCamValidation) Unwrap() error { return e.Err }

type Service struct {
	store    *Store
	nodeID   string
	tenantID string
}

func NewService(store *Store, nodeID, tenantID string) *Service {
	return &Service{store: store, nodeID: nodeID, tenantID: tenantID}
}

func (s *Service) Create(body CamWrite) (*Cam, error) {
	applyDefaults(&body)

	if err := body.Validate(); err != nil {
		return nil, ErrCamValidation{Err: err}
	}

	camID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("camera: generate cam_id: %w", err)
	}

	siteID := body.Site.SiteID
	if siteID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("camera: generate site_id: %w", err)
		}
		siteID = id.String()
	}

	profiles := make([]CamProfile, len(body.Profiles))
	for i, pw := range body.Profiles {
		pid, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("camera: generate profile_id: %w", err)
		}
		profiles[i] = CamProfile{
			ProfileID:       pid.String(),
			Name:            pw.Name,
			StreamPath:      pw.StreamPath,
			CodecHint:       pw.CodecHint,
			ResolutionHint:  pw.ResolutionHint,
			BitrateKbpsHint: pw.BitrateKbpsHint,
			FramerateHint:   pw.FramerateHint,
			IsDefault:       pw.IsDefault,
		}
	}

	probeID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("camera: generate probe_id: %w", err)
	}

	now := time.Now()
	cam := &Cam{
		CamID:    camID.String(),
		TenantID: s.tenantID,
		Identity: body.Identity,
		Site: CamSite{
			SiteID:  siteID,
			GroupID: body.Site.GroupID,
			Labels:  body.Site.Labels,
		},
		Endpoint:     body.Endpoint,
		Profiles:     profiles,
		Capabilities: body.Capabilities,
		Health: CamHealth{
			Status: HealthStatusUnknown,
			Probe: HealthProbePointer{
				ProbeID:     probeID.String(),
				CadenceMS:   DefaultProbeCadenceMS,
				OwnerNodeID: s.nodeID,
			},
			ConsecutiveFailures: 0,
		},
		CreatedAt: now,
		UpdatedAt: now,
		Version:   0,
	}

	var creds *CamCredentialsWrite
	if body.Credentials != nil {
		cw := *body.Credentials
		creds = &cw
		cam.Credentials = &CamCredentials{
			Scheme:      body.Credentials.Scheme,
			Username:    body.Credentials.Username,
			PasswordRef: cam.CamID + ":creds",
		}
	}

	if err := cam.Validate(); err != nil {
		return nil, ErrCamValidation{Err: err}
	}

	if err := s.store.Save(cam, creds); err != nil {
		return nil, fmt.Errorf("camera: save: %w", err)
	}

	return cam, nil
}

func (s *Service) Get(camID string) (*Cam, error) {
	cam, ok := s.store.Get(camID)
	if !ok {
		return nil, ErrCamNotFound
	}
	return cam, nil
}

func (s *Service) List() []*Cam {
	return s.store.List()
}

func (s *Service) Delete(camID string) error {
	if !s.store.Delete(camID) {
		return ErrCamNotFound
	}
	return nil
}

func applyDefaults(body *CamWrite) {
	if body.Endpoint.ConnectTimeoutMS == 0 {
		body.Endpoint.ConnectTimeoutMS = DefaultConnectTimeoutMS
	}
	if body.Endpoint.ReadTimeoutMS == 0 {
		body.Endpoint.ReadTimeoutMS = DefaultReadTimeoutMS
	}
	if body.Endpoint.Transport == "" {
		body.Endpoint.Transport = EndpointTransportAuto
	}
}
