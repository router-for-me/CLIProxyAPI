package common

import "os"

type NodeIdentity struct {
	Name                    string `json:"name"`
	Source                  string `json:"source"`
	ManuallyConfigured      bool   `json:"manually_configured"`
	ShouldConfigureManually bool   `json:"should_configure_manually"`
}

func initNodeNameIdentity() {
	if envNodeName := os.Getenv("NODE_NAME"); envNodeName != "" {
		NodeName = envNodeName
		NodeNameSource = NodeNameSourceManual
		NodeNameManuallyConfigured = true
		return
	}

	hostname, _ := os.Hostname()
	NodeName = hostname
	NodeNameSource = NodeNameSourceHostname
	NodeNameManuallyConfigured = false
}

func GetNodeIdentity() NodeIdentity {
	return NodeIdentity{
		Name:                    NodeName,
		Source:                  NodeNameSource,
		ManuallyConfigured:      NodeNameManuallyConfigured,
		ShouldConfigureManually: !NodeNameManuallyConfigured,
	}
}
