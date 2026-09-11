package devin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// DevinGetUserStatusPath is the Connect-RPC unary endpoint for seat management and quota.
	DevinGetUserStatusPath = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"

	devinFingerprintHexLen = 732
)

// DevinUserStatus contains plan, quota, and account metadata returned by GetUserStatus.
type DevinUserStatus struct {
	Email                       string    `json:"email,omitempty"`
	UserName                    string    `json:"user_name,omitempty"`
	UserID                      string    `json:"user_id,omitempty"`
	TeamID                      string    `json:"team_id,omitempty"`
	OrgID                       string    `json:"org_id,omitempty"`
	OrgName                     string    `json:"org_name,omitempty"`
	Plan                        string    `json:"plan,omitempty"`
	DailyQuotaRemainingPercent  int64     `json:"daily_quota_remaining_percent"`
	WeeklyQuotaRemainingPercent int64     `json:"weekly_quota_remaining_percent"`
	DailyQuotaResetAt           time.Time `json:"daily_quota_reset_at,omitempty"`
	WeeklyQuotaResetAt          time.Time `json:"weekly_quota_reset_at,omitempty"`
	PlanStart                   time.Time `json:"plan_start,omitempty"`
	PlanEnd                     time.Time `json:"plan_end,omitempty"`
}

// GenerateDeviceFingerprint generates a stable 732-character hex device fingerprint.
func GenerateDeviceFingerprint(seed string) string {
	if seed == "" {
		seed = uuid.New().String()
	}
	var sb strings.Builder
	counter := 0
	for sb.Len() < devinFingerprintHexLen {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", seed, counter)))
		sb.WriteString(hex.EncodeToString(h[:]))
		counter++
	}
	return sb.String()[:devinFingerprintHexLen]
}

// BuildGetUserStatusRequest serializes a Connect-RPC GetUserStatus request protobuf.
func BuildGetUserStatusRequest(sessionToken, deviceFingerprint string) []byte {
	if deviceFingerprint == "" {
		deviceFingerprint = GenerateDeviceFingerprint(sessionToken)
	}

	var f1Bytes []byte
	f1Bytes = protowire.AppendTag(f1Bytes, 1, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, "chisel")

	f1Bytes = protowire.AppendTag(f1Bytes, 2, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, "3000.10.21")

	f1Bytes = protowire.AppendTag(f1Bytes, 3, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, sessionToken)

	f1Bytes = protowire.AppendTag(f1Bytes, 4, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, "en")

	f1Bytes = protowire.AppendTag(f1Bytes, 5, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, runtime.GOOS)

	f1Bytes = protowire.AppendTag(f1Bytes, 7, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, "3000.10.21")

	f1Bytes = protowire.AppendTag(f1Bytes, 12, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, "chisel")

	f1Bytes = protowire.AppendTag(f1Bytes, 31, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, deviceFingerprint)

	var reqBytes []byte
	reqBytes = protowire.AppendTag(reqBytes, 1, protowire.BytesType)
	reqBytes = protowire.AppendBytes(reqBytes, f1Bytes)

	return reqBytes
}

// ParseGetUserStatusResponse decodes the binary protobuf returned by GetUserStatus.
func ParseGetUserStatusResponse(data []byte) (*DevinUserStatus, error) {
	if len(data) == 0 {
		return nil, errors.New("empty response data")
	}

	status := &DevinUserStatus{}
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		rem = rem[n:]

		if num == 1 && wireType == protowire.BytesType {
			// Field 1: user_status
			userStatusBytes, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			rem = rem[m:]

			parseUserStatus(userStatusBytes, status)
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			rem = rem[m:]
		}
	}

	return status, nil
}

func parseUserStatus(data []byte, status *DevinUserStatus) {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return
		}
		rem = rem[n:]

		if wireType == protowire.BytesType {
			bytesVal, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return
			}
			rem = rem[m:]

			switch num {
			case 3:
				status.UserName = string(bytesVal)
			case 5:
				status.TeamID = string(bytesVal)
			case 7:
				status.Email = string(bytesVal)
			case 13:
				parsePlanStatus(bytesVal, status)
			case 36:
				status.UserID = string(bytesVal)
			}
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m < 0 {
				return
			}
			rem = rem[m:]
		}
	}
}

func parsePlanStatus(data []byte, status *DevinUserStatus) {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return
		}
		rem = rem[n:]

		switch wireType {
		case protowire.BytesType:
			bytesVal, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return
			}
			rem = rem[m:]

			switch num {
			case 1:
				parsePlanInfo(bytesVal, status)
			case 2:
				sec := parseSecondsSubfield(bytesVal)
				if sec > 0 {
					status.PlanStart = time.Unix(sec, 0).UTC()
				}
			case 3:
				sec := parseSecondsSubfield(bytesVal)
				if sec > 0 {
					status.PlanEnd = time.Unix(sec, 0).UTC()
				}
			}

		case protowire.VarintType:
			val, m := protowire.ConsumeVarint(rem)
			if m < 0 {
				return
			}
			rem = rem[m:]

			switch num {
			case 14:
				status.DailyQuotaRemainingPercent = int64(val)
			case 15:
				status.WeeklyQuotaRemainingPercent = int64(val)
			case 17:
				if val > 0 {
					status.DailyQuotaResetAt = time.Unix(int64(val), 0).UTC()
				}
			case 18:
				if val > 0 {
					status.WeeklyQuotaResetAt = time.Unix(int64(val), 0).UTC()
				}
			}

		default:
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m < 0 {
				return
			}
			rem = rem[m:]
		}
	}
}

func parsePlanInfo(data []byte, status *DevinUserStatus) {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return
		}
		rem = rem[n:]

		if wireType == protowire.BytesType {
			bytesVal, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return
			}
			rem = rem[m:]

			switch num {
			case 2:
				status.Plan = string(bytesVal)
			case 33:
				parsePlanInfoOrg(bytesVal, status)
			}
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m < 0 {
				return
			}
			rem = rem[m:]
		}
	}
}

func parsePlanInfoOrg(data []byte, status *DevinUserStatus) {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return
		}
		rem = rem[n:]

		if wireType == protowire.BytesType {
			bytesVal, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				return
			}
			rem = rem[m:]

			switch num {
			case 4:
				status.OrgID = string(bytesVal)
			case 8:
				status.OrgName = string(bytesVal)
			}
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m < 0 {
				return
			}
			rem = rem[m:]
		}
	}
}

func parseSecondsSubfield(data []byte) int64 {
	rem := data
	for len(rem) > 0 {
		num, wireType, n := protowire.ConsumeTag(rem)
		if n < 0 {
			return 0
		}
		rem = rem[n:]

		if wireType == protowire.VarintType {
			val, m := protowire.ConsumeVarint(rem)
			if m < 0 {
				return 0
			}
			rem = rem[m:]
			if num == 1 {
				return int64(val)
			}
		} else {
			m := protowire.ConsumeFieldValue(num, wireType, rem)
			if m < 0 {
				return 0
			}
			rem = rem[m:]
		}
	}
	return 0
}

// FetchUserStatus queries Connect-RPC endpoint GetUserStatus for quota, plan, and user details.
func (s *DevinAuthService) FetchUserStatus(ctx context.Context, sessionToken, deviceSeed string) (*DevinUserStatus, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("devin auth service: uninitialized client")
	}

	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return nil, errors.New("devin auth service: session token is required")
	}

	reqBody := BuildGetUserStatusRequest(sessionToken, GenerateDeviceFingerprint(deviceSeed))

	endpoint := fmt.Sprintf("%s%s", strings.TrimRight(s.serverBaseURL, "/"), DevinGetUserStatusPath)
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(reqBody)))
	if errReq != nil {
		return nil, errReq
	}

	req.Header.Set("Authorization", fmt.Sprintf("Basic %s-%s", sessionToken, sessionToken))
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Content-Type", "application/proto")

	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, errRead
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("devin seat management error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	return ParseGetUserStatusResponse(respBytes)
}
