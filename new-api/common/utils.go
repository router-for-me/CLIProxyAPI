package common

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/big"
	"math/rand"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

func OpenBrowser(url string) {
	var err error

	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	}
	if err != nil {
		log.Println(err)
	}
}

func GetIp() (ip string) {
	ips, err := net.InterfaceAddrs()
	if err != nil {
		log.Println(err)
		return ip
	}

	for _, a := range ips {
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				ip = ipNet.IP.String()
				if strings.HasPrefix(ip, "10") {
					return
				}
				if strings.HasPrefix(ip, "172") {
					return
				}
				if strings.HasPrefix(ip, "192.168") {
					return
				}
				ip = ""
			}
		}
	}
	return
}

func GetNetworkIps() []string {
	var networkIps []string
	ips, err := net.InterfaceAddrs()
	if err != nil {
		log.Println(err)
		return networkIps
	}

	for _, a := range ips {
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				ip := ipNet.IP.String()
				// Include common private network ranges
				if strings.HasPrefix(ip, "10.") ||
					strings.HasPrefix(ip, "172.") ||
					strings.HasPrefix(ip, "192.168.") {
					networkIps = append(networkIps, ip)
				}
			}
		}
	}
	return networkIps
}

// IsRunningInContainer detects if the application is running inside a container
func IsRunningInContainer() bool {
	// Method 1: Check for .dockerenv file (Docker containers)
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// Method 2: Check cgroup for container indicators
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		if strings.Contains(content, "docker") ||
			strings.Contains(content, "containerd") ||
			strings.Contains(content, "kubepods") ||
			strings.Contains(content, "/lxc/") {
			return true
		}
	}

	// Method 3: Check environment variables commonly set by container runtimes
	containerEnvVars := []string{
		"KUBERNETES_SERVICE_HOST",
		"DOCKER_CONTAINER",
		"container",
	}

	for _, envVar := range containerEnvVars {
		if os.Getenv(envVar) != "" {
			return true
		}
	}

	// Method 4: Check if init process is not the traditional init
	if data, err := os.ReadFile("/proc/1/comm"); err == nil {
		comm := strings.TrimSpace(string(data))
		// In containers, process 1 is often not "init" or "systemd"
		if comm != "init" && comm != "systemd" {
			// Additional check: if it's a common container entrypoint
			if strings.Contains(comm, "docker") ||
				strings.Contains(comm, "containerd") ||
				strings.Contains(comm, "runc") {
				return true
			}
		}
	}

	return false
}

var sizeKB = 1024
var sizeMB = sizeKB * 1024
var sizeGB = sizeMB * 1024

func Bytes2Size(num int64) string {
	numStr := ""
	unit := "B"
	if num/int64(sizeGB) > 1 {
		numStr = fmt.Sprintf("%.2f", float64(num)/float64(sizeGB))
		unit = "GB"
	} else if num/int64(sizeMB) > 1 {
		numStr = fmt.Sprintf("%d", int(float64(num)/float64(sizeMB)))
		unit = "MB"
	} else if num/int64(sizeKB) > 1 {
		numStr = fmt.Sprintf("%d", int(float64(num)/float64(sizeKB)))
		unit = "KB"
	} else {
		numStr = fmt.Sprintf("%d", num)
	}
	return numStr + " " + unit
}

func Seconds2Time(num int) (time string) {
	if num/31104000 > 0 {
		time += strconv.Itoa(num/31104000) + " 年 "
		num %= 31104000
	}
	if num/2592000 > 0 {
		time += strconv.Itoa(num/2592000) + " 个月 "
		num %= 2592000
	}
	if num/86400 > 0 {
		time += strconv.Itoa(num/86400) + " 天 "
		num %= 86400
	}
	if num/3600 > 0 {
		time += strconv.Itoa(num/3600) + " 小时 "
		num %= 3600
	}
	if num/60 > 0 {
		time += strconv.Itoa(num/60) + " 分钟 "
		num %= 60
	}
	time += strconv.Itoa(num) + " 秒"
	return
}

func Interface2String(inter any) string {
	switch inter.(type) {
	case string:
		return inter.(string)
	case int:
		return fmt.Sprintf("%d", inter.(int))
	case float64:
		return strconv.FormatFloat(inter.(float64), 'f', -1, 64)
	case bool:
		if inter.(bool) {
			return "true"
		} else {
			return "false"
		}
	case nil:
		return ""
	}
	return fmt.Sprintf("%v", inter)
}

func UnescapeHTML(x string) any {
	return template.HTML(x)
}

func IntMax(a int, b int) int {
	if a >= b {
		return a
	} else {
		return b
	}
}

func GetUUID() string {
	code := uuid.New().String()
	code = strings.Replace(code, "-", "", -1)
	return code
}

const keyChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func GenerateRandomCharsKey(length int) (string, error) {
	b := make([]byte, length)
	maxI := big.NewInt(int64(len(keyChars)))

	for i := range b {
		n, err := crand.Int(crand.Reader, maxI)
		if err != nil {
			return "", err
		}
		b[i] = keyChars[n.Int64()]
	}

	return string(b), nil
}

func GenerateRandomKey(length int) (string, error) {
	bytes := make([]byte, length*3/4) // 对于48位的输出，这里应该是36
	if _, err := crand.Read(bytes); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(bytes), nil
}

func GenerateKey() (string, error) {
	//rand.Seed(time.Now().UnixNano())
	return GenerateRandomCharsKey(48)
}

func GetRandomInt(max int) int {
	//rand.Seed(time.Now().UnixNano())
	return rand.Intn(max)
}

func GetTimestamp() int64 {
	return time.Now().Unix()
}

func GetTimeString() string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s%09d", now.Format("20060102150405"), now.UnixNano()%1e9)
}

var requestIdPrefix = func() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Path != "" {
		h := sha256.Sum256([]byte(bi.Main.Path))
		return hex.EncodeToString(h[:4])
	}
	return GetRandomString(8)
}()

func NewRequestId() string {
	return GetTimeString() + requestIdPrefix + GetRandomString(8)
}

func Max(a int, b int) int {
	if a >= b {
		return a
	} else {
		return b
	}
}

func MessageWithRequestId(message string, id string) string {
	return fmt.Sprintf("%s (request id: %s)", message, id)
}

func RandomSleep() {
	// Sleep for 0-3000 ms
	time.Sleep(time.Duration(rand.Intn(3000)) * time.Millisecond)
}

func GetPointer[T any](v T) *T {
	return &v
}

func Any2Type[T any](data any) (T, error) {
	var zero T
	bytes, err := json.Marshal(data)
	if err != nil {
		return zero, err
	}
	var res T
	err = json.Unmarshal(bytes, &res)
	if err != nil {
		return zero, err
	}
	return res, nil
}

// SaveTmpFile saves data to a temporary file. The filename would be apppended with a random string.
func SaveTmpFile(filename string, data io.Reader) (string, error) {
	f, err := os.CreateTemp(os.TempDir(), filename)
	if err != nil {
		return "", errors.Wrapf(err, "failed to create temporary file %s", filename)
	}
	defer f.Close()

	_, err = io.Copy(f, data)
	if err != nil {
		return "", errors.Wrapf(err, "failed to copy data to temporary file %s", filename)
	}

	return f.Name(), nil
}

// BuildURL concatenates base and endpoint, returns the complete url string
func BuildURL(base string, endpoint string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base + endpoint
	}
	end := endpoint
	if end == "" {
		end = "/"
	}
	ref, err := url.Parse(end)
	if err != nil {
		return base + endpoint
	}
	return u.ResolveReference(ref).String()
}
