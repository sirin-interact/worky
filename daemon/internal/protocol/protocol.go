// Package protocol defines the wire protocol shared by the daemon and the UI.
// The TypeScript side lives in packages/protocol. Change both together.
package protocol

// Version is the wire protocol version.
const Version = 1

// Health is the response of GET /v1/health.
type Health struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}
