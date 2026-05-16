package protocol

import (
	"errors"
	"fmt"
	"sort"
)

// Class names the boundary role a protocol is allowed to occupy.
type Class string

const (
	ClassIngest            Class = "ingest"
	ClassControl           Class = "control"
	ClassInternalTransport Class = "internal_transport"
	ClassDelivery          Class = "delivery"
	ClassRestream          Class = "restream"
	ClassStorage           Class = "storage"
)

// ID is a stable, role-scoped protocol identifier.
//
// IDs include the class namespace so the same wire protocol can be used at
// different media boundaries without breaking the one-ID-one-class invariant.
type ID string

const (
	IDIngestRTSP           ID = "ingest.rtsp"
	IDControlHTTP          ID = "control.http"
	IDInternalRelayFLV     ID = "internal_transport.relay_flv"
	IDInternalGRPCStream   ID = "internal_transport.grpc_stream"
	IDDeliveryWebRTC       ID = "delivery.webrtc"
	IDDeliveryHLS          ID = "delivery.hls"
	IDRestreamRTSP         ID = "restream.rtsp"
	IDStorageRecorder      ID = "storage.recorder"
	IDStorageObjectStorage ID = "storage.object_storage"
)

var classByID = map[ID]Class{
	IDIngestRTSP:           ClassIngest,
	IDControlHTTP:          ClassControl,
	IDInternalRelayFLV:     ClassInternalTransport,
	IDInternalGRPCStream:   ClassInternalTransport,
	IDDeliveryWebRTC:       ClassDelivery,
	IDDeliveryHLS:          ClassDelivery,
	IDRestreamRTSP:         ClassRestream,
	IDStorageRecorder:      ClassStorage,
	IDStorageObjectStorage: ClassStorage,
}

var validClasses = map[Class]struct{}{
	ClassIngest:            {},
	ClassControl:           {},
	ClassInternalTransport: {},
	ClassDelivery:          {},
	ClassRestream:          {},
	ClassStorage:           {},
}

// AdapterMetadata describes an adapter at a protocol boundary.
type AdapterMetadata struct {
	ID           ID
	Class        Class
	WireName     string
	DisplayName  string
	Capabilities map[string]any
	Priority     int
}

// Adapter is the common metadata surface every protocol adapter should expose.
type Adapter interface {
	ProtocolMetadata() AdapterMetadata
}

// IngestAdapter marks adapters that validate a camera source and emit normalized frames.
type IngestAdapter interface {
	Adapter
	IngestAdapter()
}

// DeliveryAdapter marks adapters that expose viewer-facing playback endpoints.
type DeliveryAdapter interface {
	Adapter
	DeliveryAdapter()
}

// RestreamAdapter marks adapters that expose machine-facing egress endpoints.
type RestreamAdapter interface {
	Adapter
	RestreamAdapter()
}

// StorageAdapter marks adapters that persist downstream media.
type StorageAdapter interface {
	Adapter
	StorageAdapter()
}

// Registry stores validated adapter metadata and deterministic class lookups.
type Registry struct {
	byID        map[ID]AdapterMetadata
	idsByClass  map[Class][]ID
	wireByClass map[Class]map[string]ID
}

// ErrUnsupportedProtocol is returned when no adapter can satisfy a requested class.
var ErrUnsupportedProtocol = errors.New("unsupported_protocol")

// UnsupportedProtocolError carries structured unsupported-protocol context.
type UnsupportedProtocolError struct {
	Class     Class
	Requested []string
}

func (e UnsupportedProtocolError) Error() string {
	return fmt.Sprintf("%s: no shared %s protocol", ErrUnsupportedProtocol, e.Class)
}

func (e UnsupportedProtocolError) Is(target error) bool {
	return target == ErrUnsupportedProtocol
}

// Valid reports whether c is one of the Wave-1 taxonomy classes.
func (c Class) Valid() bool {
	_, ok := validClasses[c]
	return ok
}

// Known reports whether id is one of the protocol IDs defined by this package.
func (id ID) Known() bool {
	_, ok := classByID[id]
	return ok
}

// Class returns the class assigned to a known ID.
func (id ID) Class() (Class, bool) {
	class, ok := classByID[id]
	return class, ok
}

// Validate checks that metadata obeys the taxonomy invariants.
func (m AdapterMetadata) Validate() error {
	if m.ID == "" {
		return errors.New("protocol id is required")
	}
	if m.Class == "" {
		return errors.New("protocol class is required")
	}
	if !m.Class.Valid() {
		return fmt.Errorf("protocol class %q is invalid", m.Class)
	}
	if m.WireName == "" {
		return errors.New("protocol wire name is required")
	}
	if m.Priority < 0 {
		return errors.New("protocol priority must be non-negative")
	}
	if expected, ok := m.ID.Class(); ok && expected != m.Class {
		return fmt.Errorf("protocol id %q belongs to class %q, not %q", m.ID, expected, m.Class)
	}
	return nil
}

// DefaultAdapters returns the Wave-1 adapter metadata set.
func DefaultAdapters() []AdapterMetadata {
	return []AdapterMetadata{
		{
			ID:          IDIngestRTSP,
			Class:       ClassIngest,
			WireName:    "rtsp",
			DisplayName: "RTSP ingest",
			Capabilities: map[string]any{
				"source": "camera",
			},
			Priority: 10,
		},
		{
			ID:          IDControlHTTP,
			Class:       ClassControl,
			WireName:    "http",
			DisplayName: "HTTP control",
			Capabilities: map[string]any{
				"orchestration": true,
			},
			Priority: 10,
		},
		{
			ID:          IDInternalRelayFLV,
			Class:       ClassInternalTransport,
			WireName:    "relay_flv",
			DisplayName: "Relay FLV",
			Capabilities: map[string]any{
				"service_to_service": true,
			},
			Priority: 10,
		},
		{
			ID:          IDDeliveryWebRTC,
			Class:       ClassDelivery,
			WireName:    "webrtc",
			DisplayName: "WebRTC delivery",
			Capabilities: map[string]any{
				"reconnect":      true,
				"supports_audio": false,
			},
			Priority: 10,
		},
		{
			ID:          IDDeliveryHLS,
			Class:       ClassDelivery,
			WireName:    "hls",
			DisplayName: "HLS delivery",
			Capabilities: map[string]any{
				"reconnect":      true,
				"supports_audio": false,
			},
			Priority: 20,
		},
		{
			ID:          IDRestreamRTSP,
			Class:       ClassRestream,
			WireName:    "rtsp",
			DisplayName: "RTSP restream",
			Capabilities: map[string]any{
				"machine_egress": true,
			},
			Priority: 10,
		},
		{
			ID:          IDStorageRecorder,
			Class:       ClassStorage,
			WireName:    "recorder",
			DisplayName: "Recorder storage",
			Capabilities: map[string]any{
				"persistence": true,
			},
			Priority: 10,
		},
	}
}

// DefaultRegistry returns a registry populated with the Wave-1 adapter metadata.
func DefaultRegistry() (*Registry, error) {
	return NewRegistry(DefaultAdapters()...)
}

// MustDefaultRegistry returns the Wave-1 registry or panics if package defaults are invalid.
func MustDefaultRegistry() *Registry {
	registry, err := DefaultRegistry()
	if err != nil {
		panic(err)
	}
	return registry
}

// NewRegistry validates adapter metadata and indexes it by ID, class, and wire name.
func NewRegistry(adapters ...AdapterMetadata) (*Registry, error) {
	registry := &Registry{
		byID:        make(map[ID]AdapterMetadata, len(adapters)),
		idsByClass:  make(map[Class][]ID),
		wireByClass: make(map[Class]map[string]ID),
	}

	for _, adapter := range adapters {
		if err := registry.register(adapter); err != nil {
			return nil, err
		}
	}

	for class := range registry.idsByClass {
		sort.SliceStable(registry.idsByClass[class], func(i, j int) bool {
			left := registry.byID[registry.idsByClass[class][i]]
			right := registry.byID[registry.idsByClass[class][j]]
			if left.Priority != right.Priority {
				return left.Priority < right.Priority
			}
			return left.ID < right.ID
		})
	}

	return registry, nil
}

func (r *Registry) register(adapter AdapterMetadata) error {
	if err := adapter.Validate(); err != nil {
		return err
	}
	if _, exists := r.byID[adapter.ID]; exists {
		return fmt.Errorf("protocol id %q is already registered", adapter.ID)
	}
	if r.wireByClass[adapter.Class] == nil {
		r.wireByClass[adapter.Class] = make(map[string]ID)
	}
	if existing, exists := r.wireByClass[adapter.Class][adapter.WireName]; exists {
		return fmt.Errorf("protocol wire name %q for class %q is already registered by %q", adapter.WireName, adapter.Class, existing)
	}

	adapter.Capabilities = cloneCapabilities(adapter.Capabilities)
	r.byID[adapter.ID] = adapter
	r.idsByClass[adapter.Class] = append(r.idsByClass[adapter.Class], adapter.ID)
	r.wireByClass[adapter.Class][adapter.WireName] = adapter.ID
	return nil
}

// Lookup returns adapter metadata by protocol ID.
func (r *Registry) Lookup(id ID) (AdapterMetadata, bool) {
	adapter, ok := r.byID[id]
	if !ok {
		return AdapterMetadata{}, false
	}
	adapter.Capabilities = cloneCapabilities(adapter.Capabilities)
	return adapter, true
}

// LookupWire returns adapter metadata by class and external wire protocol name.
func (r *Registry) LookupWire(class Class, wireName string) (AdapterMetadata, bool) {
	id, ok := r.wireByClass[class][wireName]
	if !ok {
		return AdapterMetadata{}, false
	}
	return r.Lookup(id)
}

// AdaptersForClass returns all adapters registered to a class in deterministic order.
func (r *Registry) AdaptersForClass(class Class) []AdapterMetadata {
	ids := r.idsByClass[class]
	adapters := make([]AdapterMetadata, 0, len(ids))
	for _, id := range ids {
		adapter, _ := r.Lookup(id)
		adapters = append(adapters, adapter)
	}
	return adapters
}

// IDsForClass returns protocol IDs registered to a class in deterministic order.
func (r *Registry) IDsForClass(class Class) []ID {
	ids := r.idsByClass[class]
	copied := make([]ID, len(ids))
	copy(copied, ids)
	return copied
}

// SupportedWireNames returns external protocol names for a class in deterministic order.
func (r *Registry) SupportedWireNames(class Class) []string {
	adapters := r.AdaptersForClass(class)
	names := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		names = append(names, adapter.WireName)
	}
	return names
}

// NegotiateDelivery chooses the first server-preferred delivery adapter present
// in the client's supported protocol list.
func (r *Registry) NegotiateDelivery(clientSupported []string) (AdapterMetadata, error) {
	return r.Negotiate(ClassDelivery, clientSupported)
}

// Negotiate chooses the first server-preferred adapter for class present in the
// client's supported protocol list. Entries may be wire names or role-scoped IDs.
func (r *Registry) Negotiate(class Class, clientSupported []string) (AdapterMetadata, error) {
	supported := make(map[string]struct{}, len(clientSupported))
	for _, protocol := range clientSupported {
		if protocol != "" {
			supported[protocol] = struct{}{}
		}
	}

	for _, adapter := range r.AdaptersForClass(class) {
		if _, ok := supported[adapter.WireName]; ok {
			return adapter, nil
		}
		if _, ok := supported[string(adapter.ID)]; ok {
			return adapter, nil
		}
	}

	requested := make([]string, len(clientSupported))
	copy(requested, clientSupported)
	return AdapterMetadata{}, UnsupportedProtocolError{
		Class:     class,
		Requested: requested,
	}
}

func cloneCapabilities(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
