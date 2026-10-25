package kiro

import (
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Streaming and management clients report different AWS SDK versions.
const (
	// DefaultIDEVersion is reported when the credential does not pin a
	// version through the "ide_version" metadata key.
	DefaultIDEVersion = "1.4.26"
	// nodeVersion mirrors the Node.js runtime bundled with the Kiro IDE.
	nodeVersion = "22.22.0"
	// desktopAuthIDEVersion is pinned by the Kiro desktop auth client used
	// for social login and refresh; it does not track the app version.
	desktopAuthIDEVersion = "0.6.18"
	runtimeSDKVersion     = "1.0.39"
	managementSDKVersion  = "1.0.0"
)

// IDEVersion returns the Kiro IDE version reported to upstream services.
func IDEVersion(metadata map[string]any) string {
	if version := String(metadata, "ide_version"); version != "" {
		return version
	}
	return DefaultIDEVersion
}

// EnsureMachineID stores a credential-specific identity when absent. Deriving
// it from account data also keeps read-only file reloads stable before a save.
func EnsureMachineID(metadata map[string]any) string {
	if id := String(metadata, "machine_id"); id != "" {
		return id
	}
	id := machineIDOrDerived(metadata)
	if id == uuid.Nil.String() {
		id = uuid.NewString()
	}
	if metadata != nil {
		metadata["machine_id"] = id
	}
	return id
}

// Request construction must not mutate metadata shared by concurrent requests.
func machineIDOrDerived(metadata map[string]any) string {
	if id := String(metadata, "machine_id"); id != "" {
		return id
	}
	if _, canonical := metadata["machine_id"]; !canonical {
		if id := String(metadata, "machineId"); id != "" {
			return id
		}
	}
	for _, field := range [][2]string{{"email", "email"}, {"refresh_token", "refreshToken"}, {"access_token", "accessToken"}} {
		value := String(metadata, field[0])
		if _, canonical := metadata[field[0]]; !canonical {
			value = String(metadata, field[1])
		}
		if value == "" {
			continue
		}
		if field[0] == "email" {
			value = strings.ToLower(value)
		}
		seed := "kiro\x00" + strings.ToLower(String(metadata, "provider")) + "\x00" + field[0] + "\x00" + value
		return uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
	}
	return uuid.Nil.String()
}

// RuntimeUserAgent mimics the IDE streaming client (generateAssistantResponse).
func RuntimeUserAgent(metadata map[string]any) string {
	return sdkUserAgent(metadata, runtimeSDKVersion, "codewhispererstreaming", "m/N", true)
}

// RuntimeXAmzUserAgent mirrors the x-amz-user-agent sent with runtime requests.
func RuntimeXAmzUserAgent(metadata map[string]any) string {
	return sdkClientIdentity(metadata, runtimeSDKVersion)
}

// ManagementUserAgent mimics the IDE management client (getUsageLimits).
func ManagementUserAgent(metadata map[string]any) string {
	return sdkUserAgent(metadata, managementSDKVersion, "codewhispererruntime", "m/N,E", true)
}

// ManagementXAmzUserAgent mirrors the x-amz-user-agent sent with management requests.
func ManagementXAmzUserAgent(metadata map[string]any) string {
	return sdkClientIdentity(metadata, managementSDKVersion)
}

// ControlPlaneUserAgent mimics the IDE control-plane client
// (ListAvailableProfiles/ListAvailableModels); it carries no KiroIDE suffix.
func ControlPlaneUserAgent(metadata map[string]any) string {
	return sdkUserAgent(metadata, managementSDKVersion, "kirocontrolplanebearer", "m/N,E", false)
}

// ControlPlaneXAmzUserAgent mirrors the plain AWS SDK identity the
// control-plane client reports in x-amz-user-agent.
func ControlPlaneXAmzUserAgent() string {
	return "aws-sdk-js/" + managementSDKVersion
}

// DesktopAuthUserAgent mimics the pinned desktop auth client used by the
// social login and refresh endpoints under auth.desktop.kiro.dev.
func DesktopAuthUserAgent(metadata map[string]any) string {
	return fmt.Sprintf("KiroIDE-%s-%s", desktopAuthIDEVersion, machineIDOrDerived(metadata))
}

func sdkClientIdentity(metadata map[string]any, sdkVersion string) string {
	return fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s-%s", sdkVersion, IDEVersion(metadata), machineIDOrDerived(metadata))
}

func sdkUserAgent(metadata map[string]any, sdkVersion, api, retryMode string, withIdentity bool) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/%s#%s %s",
		sdkVersion, osPlatform(), osRelease(), nodeVersion, api, sdkVersion, retryMode)
	if withIdentity {
		fmt.Fprintf(&builder, " KiroIDE-%s-%s", IDEVersion(metadata), machineIDOrDerived(metadata))
	}
	return builder.String()
}

func osPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	default:
		return runtime.GOOS
	}
}

var osReleaseOnce = sync.OnceValue(detectOSRelease)

func osRelease() string {
	return osReleaseOnce()
}
