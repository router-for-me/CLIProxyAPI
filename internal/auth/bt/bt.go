package bt

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	CloudURL = "https://www.bt.cn"
	APIURL   = "https://api.bt.cn"
	AppID    = "bt_app_001"
)

func md5Hash(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func generateStableSID(phone string) string {
	macSeed := md5Hash(phone + ":mac")
	hostname := "bt-server-" + md5Hash(phone + ":hostname")[:8]
	cpu := "Intel Xeon Platinum 8480+"
	return md5Hash(macSeed+hostname) + md5Hash(cpu)
}

func generateStableMAC(phone string) string {
	macSeed := md5Hash(phone + ":mac")
	return fmt.Sprintf("%s:%s:%s:%s:%s:%s",
		macSeed[0:2], macSeed[2:4], macSeed[4:6], macSeed[6:8], macSeed[8:10], macSeed[10:12])
}

func decodeBase64Password(encoded string) string {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		ud, err2 := base64.URLEncoding.DecodeString(encoded)
		if err2 != nil {
			return encoded
		}
		return string(ud)
	}
	return string(decoded)
}

func hexEncode(data url.Values) string {
	return fmt.Sprintf("%x", []byte(data.Encode()))
}

func Login(phone, passwordBase64 string) (*BTTokenStorage, error) {
	password := decodeBase64Password(passwordBase64)
	sid := generateStableSID(phone)
	loginURL := APIURL + "/Auth/GetAuthToken"

	innerData := url.Values{
		"username": {phone},
		"password": {md5Hash(password)},
		"serverid": {sid},
		"os":       {"Linux"},
		"mac":      {generateStableMAC(phone)},
		"o":        {""},
	}
	payload := url.Values{"data": {hexEncode(innerData)}}

	resp, err := httpPostForm(loginURL, payload)
	if err != nil {
		return nil, fmt.Errorf("bt login request failed: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Debugf("bt auth: close response body error: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bt login returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("bt login read body failed: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("bt login parse response failed: %w", err)
	}

	dataHex, ok := result["data"].(string)
	if !ok {
		msg, _ := result["msg"].(string)
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("bt login failed: %s", msg)
	}

	decoded, err := hex.DecodeString(dataHex)
	if err != nil {
		return nil, fmt.Errorf("bt login decode hex failed: %w", err)
	}

	unescaped, err := url.QueryUnescape(string(decoded))
	if err != nil {
		return nil, fmt.Errorf("bt login unescape failed: %w", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(unescaped), &data); err != nil {
		return nil, fmt.Errorf("bt login parse data failed: %w", err)
	}

	uid, _ := data["uid"].(string)
	accessKey, _ := data["access_key"].(string)
	if uid == "" {
		floatUID, ok := data["uid"].(float64)
		if ok {
			uid = strconv.FormatFloat(floatUID, 'f', 0, 64)
		}
	}
	if uid == "" {
		return nil, fmt.Errorf("bt login: uid not found in response")
	}

	log.Infof("bt auth: login successful for phone %s", phone)
	return NewBTTokenStorage(phone, uid, accessKey, sid), nil
}

func RefreshSession(phone, passwordBase64 string) (*BTTokenStorage, error) {
	return Login(phone, passwordBase64)
}

func httpPostForm(url string, data url.Values) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 15 * time.Second}
	return client.Do(req)
}
