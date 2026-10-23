package domain

import "time"

const (
	CommandTTL               = 120 * time.Second
	CommandPayloadLimit      = 256 << 10
	QueryChunkLimit          = 64 << 10
	QueryTotalLimit          = 64 << 20
	LogEventLimit            = 64 << 10
	LogReadDefault           = 64 << 10
	LogReadLimit             = 1 << 20
	PageDefault              = 100
	PageLimit                = 500
	UICoalesce               = 250 * time.Millisecond
	InventoryInterval        = 30 * time.Second
	HeartbeatInterval        = 10 * time.Second
	HeartbeatStale           = 20 * time.Second
	HeartbeatOffline         = 35 * time.Second
	TelemetryRetention       = 5 * time.Minute
	TelemetryPendingMessages = 128
	TelemetryPendingBytes    = 4 << 20
	ExportTimeout            = 2 * time.Second
	ObservationTTL           = 5 * time.Minute
	EnrollmentTTL            = 10 * time.Minute
	DiscoveryInterval        = 5 * time.Second
	DiscoveryTTL             = 30 * time.Second
	DiscoverySearchWindow    = 30 * time.Second
	ResolutionTTL            = time.Hour
	WiFiCredentialTTL        = 5 * time.Minute
	DiagnosticRetention      = 24 * time.Hour
	CertificateLifetime      = 30 * 24 * time.Hour
	CertificateRenewal       = 20 * 24 * time.Hour
	StateBucket              = "nx_state"
	GitBucket                = "nx_git"
)
