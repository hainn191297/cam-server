package protocol

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultRegistryClassLookups(t *testing.T) {
	t.Parallel()

	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry() error = %v", err)
	}

	tests := []struct {
		class Class
		want  []ID
	}{
		{class: ClassIngest, want: []ID{IDIngestRTSP}},
		{class: ClassControl, want: []ID{IDControlHTTP}},
		{class: ClassInternalTransport, want: []ID{IDInternalRelayFLV}},
		{class: ClassDelivery, want: []ID{IDDeliveryWebRTC, IDDeliveryHLS}},
		{class: ClassRestream, want: []ID{IDRestreamRTSP}},
		{class: ClassStorage, want: []ID{IDStorageRecorder}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.class), func(t *testing.T) {
			t.Parallel()

			if got := registry.IDsForClass(tt.class); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("IDsForClass(%q) = %#v, want %#v", tt.class, got, tt.want)
			}
		})
	}
}

func TestDefaultRegistryKeepsRTSPRoleScoped(t *testing.T) {
	t.Parallel()

	registry := MustDefaultRegistry()

	ingest, ok := registry.LookupWire(ClassIngest, "rtsp")
	if !ok {
		t.Fatal(`LookupWire(ClassIngest, "rtsp") did not find adapter`)
	}
	restream, ok := registry.LookupWire(ClassRestream, "rtsp")
	if !ok {
		t.Fatal(`LookupWire(ClassRestream, "rtsp") did not find adapter`)
	}

	if ingest.ID == restream.ID {
		t.Fatalf("RTSP boundary IDs must differ, got %q for both", ingest.ID)
	}
	if ingest.Class != ClassIngest || restream.Class != ClassRestream {
		t.Fatalf("RTSP boundaries classified incorrectly: ingest=%q restream=%q", ingest.Class, restream.Class)
	}
}

func TestAdapterMetadataValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta AdapterMetadata
		want string
	}{
		{
			name: "missing id",
			meta: AdapterMetadata{Class: ClassDelivery, WireName: "webrtc"},
			want: "protocol id is required",
		},
		{
			name: "invalid class",
			meta: AdapterMetadata{ID: ID("custom.foo"), Class: Class("unknown"), WireName: "foo"},
			want: `protocol class "unknown" is invalid`,
		},
		{
			name: "missing wire name",
			meta: AdapterMetadata{ID: ID("custom.foo"), Class: ClassDelivery},
			want: "protocol wire name is required",
		},
		{
			name: "negative priority",
			meta: AdapterMetadata{ID: ID("custom.foo"), Class: ClassDelivery, WireName: "foo", Priority: -1},
			want: "protocol priority must be non-negative",
		},
		{
			name: "known id wrong class",
			meta: AdapterMetadata{ID: IDDeliveryWebRTC, Class: ClassRestream, WireName: "webrtc"},
			want: `protocol id "delivery.webrtc" belongs to class "delivery", not "restream"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.meta.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestRegistryRejectsDuplicateIDAndWireName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		adapters []AdapterMetadata
		want     string
	}{
		{
			name: "duplicate id",
			adapters: []AdapterMetadata{
				{ID: ID("custom.alpha"), Class: ClassDelivery, WireName: "alpha"},
				{ID: ID("custom.alpha"), Class: ClassDelivery, WireName: "beta"},
			},
			want: `protocol id "custom.alpha" is already registered`,
		},
		{
			name: "duplicate wire name in class",
			adapters: []AdapterMetadata{
				{ID: ID("custom.alpha"), Class: ClassDelivery, WireName: "alpha"},
				{ID: ID("custom.beta"), Class: ClassDelivery, WireName: "alpha"},
			},
			want: `protocol wire name "alpha" for class "delivery" is already registered by "custom.alpha"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewRegistry(tt.adapters...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewRegistry() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestNegotiateDeliveryIsServerPreferenceOrdered(t *testing.T) {
	t.Parallel()

	registry := MustDefaultRegistry()

	adapter, err := registry.NegotiateDelivery([]string{"hls", "webrtc"})
	if err != nil {
		t.Fatalf("NegotiateDelivery() error = %v", err)
	}
	if adapter.ID != IDDeliveryWebRTC {
		t.Fatalf("NegotiateDelivery() ID = %q, want %q", adapter.ID, IDDeliveryWebRTC)
	}

	adapter, err = registry.NegotiateDelivery([]string{string(IDDeliveryHLS)})
	if err != nil {
		t.Fatalf("NegotiateDelivery() by role-scoped ID error = %v", err)
	}
	if adapter.ID != IDDeliveryHLS {
		t.Fatalf("NegotiateDelivery() ID = %q, want %q", adapter.ID, IDDeliveryHLS)
	}
}

func TestNegotiateDeliveryUnsupportedProtocol(t *testing.T) {
	t.Parallel()

	_, err := MustDefaultRegistry().NegotiateDelivery([]string{"dash"})
	if !errors.Is(err, ErrUnsupportedProtocol) {
		t.Fatalf("NegotiateDelivery() error = %v, want ErrUnsupportedProtocol", err)
	}

	var unsupported UnsupportedProtocolError
	if !errors.As(err, &unsupported) {
		t.Fatalf("NegotiateDelivery() error = %T, want UnsupportedProtocolError", err)
	}
	if unsupported.Class != ClassDelivery {
		t.Fatalf("UnsupportedProtocolError.Class = %q, want %q", unsupported.Class, ClassDelivery)
	}
	if !reflect.DeepEqual(unsupported.Requested, []string{"dash"}) {
		t.Fatalf("UnsupportedProtocolError.Requested = %#v, want dash", unsupported.Requested)
	}
}

func TestCustomAdapterCanBeRegisteredWithoutSchemaChanges(t *testing.T) {
	t.Parallel()

	adapters := DefaultAdapters()
	adapters = append(adapters, AdapterMetadata{
		ID:          ID("delivery.llhls"),
		Class:       ClassDelivery,
		WireName:    "llhls",
		DisplayName: "Low-latency HLS delivery",
		Capabilities: map[string]any{
			"reconnect": true,
		},
		Priority: 15,
	})

	registry, err := NewRegistry(adapters...)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	if got := registry.SupportedWireNames(ClassDelivery); !reflect.DeepEqual(got, []string{"webrtc", "llhls", "hls"}) {
		t.Fatalf("SupportedWireNames(ClassDelivery) = %#v", got)
	}
}

func TestRegistryReturnsDefensiveCopies(t *testing.T) {
	t.Parallel()

	registry := MustDefaultRegistry()

	adapter, ok := registry.Lookup(IDDeliveryWebRTC)
	if !ok {
		t.Fatal("Lookup(IDDeliveryWebRTC) did not find adapter")
	}
	adapter.Capabilities["reconnect"] = false

	adapter, ok = registry.Lookup(IDDeliveryWebRTC)
	if !ok {
		t.Fatal("second Lookup(IDDeliveryWebRTC) did not find adapter")
	}
	if adapter.Capabilities["reconnect"] != true {
		t.Fatalf("registry capabilities were mutated: %#v", adapter.Capabilities)
	}

	ids := registry.IDsForClass(ClassDelivery)
	ids[0] = IDDeliveryHLS
	if got := registry.IDsForClass(ClassDelivery); !reflect.DeepEqual(got, []ID{IDDeliveryWebRTC, IDDeliveryHLS}) {
		t.Fatalf("IDsForClass(ClassDelivery) after caller mutation = %#v", got)
	}
}
