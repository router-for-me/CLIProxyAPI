package controller

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	passkeysvc "github.com/QuantumNous/new-api/service/passkey"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type passkeyTestBody struct {
	*strings.Reader
}

type passkeyDomainBegin struct {
	FlowToken string   `json:"flow_token"`
	RPIDs     []string `json:"rp_ids"`
	Options   struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RPID      string `json:"rpId"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
		} `json:"publicKey"`
	} `json:"options"`
}

func TestGetStatusDoesNotExposePasskeyOrigins(t *testing.T) {
	settings := system_setting.GetPasskeySettings()
	originalSettings := *settings
	originalOptionMap := common.OptionMap
	originalServerAddress := system_setting.ServerAddress
	t.Cleanup(func() {
		*settings = originalSettings
		common.OptionMap = originalOptionMap
		system_setting.ServerAddress = originalServerAddress
	})
	common.OptionMap = map[string]string{}
	system_setting.ServerAddress = "https://www.example.com"
	*settings = system_setting.PasskeySettings{Enabled: true, RPID: "example.com"}

	for _, origins := range []string{"https://www.example.com,https://private.example.com", "", "[]"} {
		t.Run("origins="+origins, func(t *testing.T) {
			settings.Origins = origins
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)

			GetStatus(context)

			require.Equal(t, http.StatusOK, response.Code)
			var payload struct {
				Success bool           `json:"success"`
				Data    map[string]any `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			require.True(t, payload.Success)
			assert.NotContains(t, payload.Data, "passkey_origins")
			assert.NotContains(t, response.Body.String(), "private.example.com")
			assert.Equal(t, true, payload.Data["passkey_login"])
			assert.Equal(t, origins, settings.Origins)
		})
	}
}

func passkeyDomainRequest(t *testing.T, path string, payload any, identity service.AuthIdentity, origin string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	body, err := common.Marshal(payload)
	require.NoError(t, err)
	return securityEnrollmentRequest(http.MethodPost, path, string(body), "", identity, func(c *gin.Context) {
		c.Request.Header.Set("Origin", origin)
		handler(c)
	})
}

func decodePasskeyDomainBegin(t *testing.T, response *httptest.ResponseRecorder) passkeyDomainBegin {
	t.Helper()
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, response.Body.String())
	var begin passkeyDomainBegin
	require.NoError(t, common.Unmarshal(result.Data, &begin))
	return begin
}

// Reuse the existing software authenticator, replacing the domain-bound bytes
// and recomputing the signature exactly as a browser/authenticator would.
func passkeyDomainAssertion(t *testing.T, key *ecdsa.PrivateKey, challenge, rpID, origin string, userID int, verified bool) map[string]any {
	t.Helper()
	var assertion map[string]any
	require.NoError(t, common.Unmarshal(securityPasskeyResponse(t, key, challenge, false, 1, verified), &assertion))
	response := assertion["response"].(map[string]any)
	clientData, err := common.Marshal(map[string]string{"type": "webauthn.get", "challenge": challenge, "origin": origin})
	require.NoError(t, err)
	authData, err := base64.RawURLEncoding.DecodeString(response["authenticatorData"].(string))
	require.NoError(t, err)
	rpHash := sha256.Sum256([]byte(rpID))
	copy(authData, rpHash[:])
	clientHash := sha256.Sum256(clientData)
	signedHash := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, key, signedHash[:])
	require.NoError(t, err)
	response["clientDataJSON"] = base64.RawURLEncoding.EncodeToString(clientData)
	response["authenticatorData"] = base64.RawURLEncoding.EncodeToString(authData)
	response["signature"] = base64.RawURLEncoding.EncodeToString(signature)
	response["userHandle"] = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprint(userID)))
	return assertion
}

func TestPasskeyDomainsPreserveCredentialsAcrossVerificationFlows(t *testing.T) {
	for _, kind := range []string{"direct", "login factor", "sensitive action", "historical mixed case"} {
		t.Run(kind, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			key := newSecurityLoginPasskey(t, user.Id)
			settings := system_setting.GetPasskeySettings()
			legacyRPID := "www.example.com"
			if kind == "historical mixed case" {
				legacyRPID = "WWW.example.com"
			}
			settings.LegacyRPIDs = legacyRPID
			settings.Origins = "https://example.com,https://www.example.com"
			beginPath, finishPath := "/api/user/passkey/login/begin", "/api/user/passkey/login/finish"
			beginHandler, finishHandler := PasskeyLoginBegin, PasskeyLoginFinish
			request := map[string]any{"rp_id": legacyRPID}
			if kind == "login factor" {
				pending, err := service.StartLoginVerification(user, "password", nil)
				require.NoError(t, err)
				request["flow_token"] = pending.FlowToken
				beginPath, finishPath = "/api/user/login/passkey/begin", "/api/user/login/passkey/finish"
				beginHandler, finishHandler = LoginPasskeyBegin, LoginPasskeyFinish
			} else if kind == "sensitive action" {
				request["scope"] = service.VerificationScopeAccessTokenGenerate
				beginPath, finishPath = "/api/user/passkey/verify/begin", "/api/user/passkey/verify/finish"
				beginHandler, finishHandler = PasskeyVerifyBegin, PasskeyVerifyFinish
			}
			begin := decodePasskeyDomainBegin(t, passkeyDomainRequest(t, beginPath, request, identity, "https://www.example.com", beginHandler))
			assert.Equal(t, legacyRPID, begin.Options.PublicKey.RPID)
			assert.Equal(t, []string{"example.com", legacyRPID}, begin.RPIDs)
			before, err := model.GetPasskeyByUserID(user.Id)
			require.NoError(t, err)
			require.Nil(t, before.RPID)
			finish := map[string]any{"flow_token": begin.FlowToken, "credential": passkeyDomainAssertion(t, key, begin.Options.PublicKey.Challenge, legacyRPID, "https://www.example.com", user.Id, true)}
			if kind == "login factor" {
				finish["flow_token"], finish["passkey_flow_token"] = request["flow_token"], begin.FlowToken
			}
			response := passkeyDomainRequest(t, finishPath, finish, identity, "https://www.example.com", finishHandler)
			var result securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			require.True(t, result.Success, response.Body.String())
			after, err := model.GetPasskeyByUserID(user.Id)
			require.NoError(t, err)
			require.NotNil(t, after.RPID)
			assert.Equal(t, legacyRPID, *after.RPID)
			assert.Equal(t, before.CredentialID, after.CredentialID)
			assert.Equal(t, before.PublicKey, after.PublicKey)
			if kind == "sensitive action" {
				var proof service.SecurityProof
				require.NoError(t, common.Unmarshal(result.Data, &proof))
				assert.Equal(t, service.VerificationScopeAccessTokenGenerate, proof.Scope)
			}
			count, err := model.CountActiveUserSessions(user.Id, time.Now().Unix())
			require.NoError(t, err)
			replay := passkeyDomainRequest(t, finishPath, finish, identity, "https://www.example.com", finishHandler)
			require.NoError(t, common.Unmarshal(replay.Body.Bytes(), &result))
			assert.False(t, result.Success)
			afterCount, err := model.CountActiveUserSessions(user.Id, time.Now().Unix())
			require.NoError(t, err)
			assert.Equal(t, count, afterCount)
			// Once identified, a browser hint cannot override the credential's binding.
			wa, ids, err := passkeysvc.BuildLoginWebAuthn(httptest.NewRequest(http.MethodPost, beginPath, nil), "example.com", *after.RPID)
			require.NoError(t, err)
			assert.Equal(t, legacyRPID, wa.Config.RPID)
			assert.Equal(t, []string{legacyRPID}, ids)
		})
	}
}

func TestPasskeyDomainsRejectInvalidAssertions(t *testing.T) {
	for _, failure := range []string{"wrong RP ID", "unlisted origin", "origin outside RP scope", "wrong user", "bad signature", "missing user verification", "expired challenge", "removed domain", "known different domain"} {
		t.Run(failure, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			key := newSecurityLoginPasskey(t, user.Id)
			settings := system_setting.GetPasskeySettings()
			settings.LegacyRPIDs = "www.example.com"
			settings.Origins = "https://example.com,https://www.example.com"
			begin := decodePasskeyDomainBegin(t, passkeyDomainRequest(t, "/api/user/passkey/login/begin", map[string]string{"rp_id": "www.example.com"}, identity, "https://www.example.com", PasskeyLoginBegin))
			rpID, origin, handle := "www.example.com", "https://www.example.com", user.Id
			switch failure {
			case "wrong RP ID":
				rpID = "example.com"
			case "unlisted origin":
				origin = "https://evil.example.com"
			case "origin outside RP scope":
				origin = "https://example.com"
			case "wrong user":
				handle++
			case "expired challenge":
				flow, err := model.GetAuthFlow(begin.FlowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposePasskeyLogin})
				require.NoError(t, err)
				var payload struct {
					SessionData webauthn.SessionData    `json:"session_data"`
					Security    passkeysvc.FlowSecurity `json:"security"`
				}
				require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &payload))
				payload.SessionData.Expires = time.Now().Add(-time.Second)
				encoded, err := common.Marshal(payload)
				require.NoError(t, err)
				require.NoError(t, model.DB.Model(flow).Update("payload", string(encoded)).Error)
			case "removed domain":
				settings.LegacyRPIDs = ""
			case "known different domain":
				require.NoError(t, model.DB.Model(&model.PasskeyCredential{}).Where("user_id = ?", user.Id).Update("rp_id", "example.com").Error)
			}
			assertion := passkeyDomainAssertion(t, key, begin.Options.PublicKey.Challenge, rpID, origin, handle, failure != "missing user verification")
			if failure == "bad signature" {
				assertion["response"].(map[string]any)["signature"] = "AA"
			}
			response := passkeyDomainRequest(t, "/api/user/passkey/login/finish", map[string]any{"flow_token": begin.FlowToken, "credential": assertion}, identity, "https://www.example.com", PasskeyLoginFinish)
			var result securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.False(t, result.Success, response.Body.String())
			assert.Empty(t, response.Header().Values("Set-Cookie"))
			count, err := model.CountActiveUserSessions(user.Id, time.Now().Unix())
			require.NoError(t, err)
			assert.EqualValues(t, 1, count)
			stored, err := model.GetPasskeyByUserID(user.Id)
			require.NoError(t, err)
			if failure == "known different domain" {
				assert.Equal(t, "example.com", *stored.RPID)
			} else {
				assert.Nil(t, stored.RPID)
			}
		})
	}
}

func TestPasskeyDomainChoicesRespectOriginAndConfiguration(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	settings := system_setting.GetPasskeySettings()
	settings.LegacyRPIDs = "www.example.com"
	settings.Origins = "https://example.com,https://www.example.com"
	for _, test := range []struct {
		origin, hint string
		success      bool
		ids          []string
	}{
		{"https://www.example.com", "", true, []string{"example.com", "www.example.com"}},
		{"https://example.com", "", true, []string{"example.com"}},
		{"https://example.com", "www.example.com", false, nil},
		{"https://www.example.com", "unconfigured.example.com", false, nil},
		{"https://attacker.test", "", false, nil},
	} {
		response := passkeyDomainRequest(t, "/api/user/passkey/login/begin", map[string]string{"rp_id": test.hint}, identity, test.origin, PasskeyLoginBegin)
		var result securityEnrollmentResponse
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
		assert.Equal(t, test.success, result.Success, "%+v", test)
		if test.success {
			assert.Equal(t, test.ids, decodePasskeyDomainBegin(t, response).RPIDs)
		} else {
			assert.Equal(t, "PASSKEY_RP_ID_UNAVAILABLE", result.Code)
		}
	}
	settings.RPID, settings.LegacyRPIDs, settings.Origins, settings.AllowInsecureOrigin = "localhost", "", "http://localhost:3000,http://localhost:3001", true
	for _, origin := range []string{"http://localhost:3000", "http://localhost:3001"} {
		begin := decodePasskeyDomainBegin(t, passkeyDomainRequest(t, "/api/user/passkey/login/begin", nil, identity, origin, PasskeyLoginBegin))
		assert.Equal(t, "localhost", begin.Options.PublicKey.RPID)
	}
}

func setupPasskeyDomainOptions(t *testing.T) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	common.OptionMapRWMutex.RLock()
	previousOptions := maps.Clone(common.OptionMap)
	previousAddress := system_setting.ServerAddress
	common.OptionMapRWMutex.RUnlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptions
		system_setting.ServerAddress = previousAddress
		common.OptionMapRWMutex.Unlock()
	})
}

func TestPasskeyDomainRemovalProtectsExistingAndUnknownCredentials(t *testing.T) {
	for _, binding := range []string{"www.example.com", ""} {
		t.Run("binding="+binding, func(t *testing.T) {
			user, _ := setupSecurityEnrollmentTest(t)
			setupPasskeyDomainOptions(t)
			settings := system_setting.GetPasskeySettings()
			settings.LegacyRPIDs = "www.example.com"
			settings.Origins = "https://example.com,https://www.example.com"
			credential := &model.PasskeyCredential{UserID: user.Id, CredentialID: "existing", PublicKey: "key"}
			if binding != "" {
				credential.RPID = &binding
			}
			require.NoError(t, model.DB.Create(credential).Error)
			assert.Error(t, model.UpdateOption("passkey.legacy_rp_ids", ""))
			assert.Equal(t, "www.example.com", system_setting.PasskeySettingsSnapshot().LegacyRPIDs)
		})
	}
}

func TestPasskeyDomainRemovalWithoutAffectedCredentialsNeedsNoOverride(t *testing.T) {
	user, _ := setupSecurityEnrollmentTest(t)
	setupPasskeyDomainOptions(t)
	settings := system_setting.GetPasskeySettings()
	settings.LegacyRPIDs = "www.example.com,WWW.example.com"
	upper := "WWW.example.com"
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id, RPID: &upper, CredentialID: "uppercase", PublicKey: "key"}).Error)
	change, err := model.UpdatePasskeyDomainOptions(map[string]string{"passkey.legacy_rp_ids": upper}, false, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"www.example.com"}, change.RemovedRPIDs)
	assert.Zero(t, change.AffectedCredentials)
	assert.Zero(t, change.UnknownCredentials)
	assert.False(t, change.ConfirmationRequired)
	assert.Equal(t, upper, settings.LegacyRPIDs)
}

func TestPasskeySnapshotDoesNotMaterializeDefaults(t *testing.T) {
	setupSecurityEnrollmentTest(t)
	setupPasskeyDomainOptions(t)
	settings := system_setting.GetPasskeySettings()
	settings.RPID, settings.Origins = "", ""
	system_setting.ServerAddress = "https://www.example.com"
	assert.Equal(t, "www.example.com", system_setting.PasskeySettingsSnapshot().EffectiveRPID())
	assert.Empty(t, settings.RPID)
	assert.Empty(t, settings.Origins)
}

func TestPasskeyDomainPreviewConfirmationAndAudit(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	setupPasskeyDomainOptions(t)
	settings := system_setting.GetPasskeySettings()
	settings.LegacyRPIDs = "www.example.com,WWW.example.com"
	settings.Origins = "https://example.com,https://www.example.com"
	lower, upper, empty := "www.example.com", "WWW.example.com", ""
	for i, binding := range []*string{&lower, &upper, nil, &empty, &lower} {
		credential := &model.PasskeyCredential{UserID: user.Id + i, RPID: binding, CredentialID: fmt.Sprint(i), PublicKey: "key"}
		require.NoError(t, model.DB.Create(credential).Error)
		if i == 4 {
			require.NoError(t, model.DB.Delete(credential).Error)
		}
	}
	request := map[string]any{"rp_id": "example.com", "legacy_rp_ids": upper, "origins": settings.Origins, "preview": true}
	response := passkeyDomainRequest(t, "/api/option/passkey/domains", request, identity, "https://example.com", UpdatePasskeyDomains)
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, response.Body.String())
	var preview model.PasskeyDomainChange
	require.NoError(t, common.Unmarshal(result.Data, &preview))
	assert.Equal(t, []string{lower}, preview.RemovedRPIDs)
	assert.EqualValues(t, 1, preview.AffectedCredentials)
	assert.EqualValues(t, 2, preview.UnknownCredentials)
	assert.True(t, preview.ConfirmationRequired)
	require.NotEmpty(t, preview.RemovalConfirmation)
	options, err := model.AllOption()
	require.NoError(t, err)
	assert.Empty(t, options, "preview must not materialize default rows")
	assert.Equal(t, "www.example.com,WWW.example.com", settings.LegacyRPIDs)
	assert.ErrorIs(t, model.UpdateOptionsBulk(map[string]string{"passkey.legacy_rp_ids": upper, "Notice": "must roll back"}), model.ErrPasskeyDomainRemovalConfirmation)
	blocked := passkeyDomainRequest(t, "/api/option/", map[string]string{"key": "passkey.legacy_rp_ids", "value": upper}, identity, "https://example.com", UpdateOption)
	assert.Equal(t, http.StatusConflict, blocked.Code)
	assert.Contains(t, blocked.Body.String(), "PASSKEY_RP_ID_REMOVAL_CONFIRMATION_REQUIRED")
	request["preview"] = false
	request["removal_confirmation"] = preview.RemovalConfirmation
	// A new unknown credential makes the reviewed impact stale.
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id + 10, CredentialID: "new", PublicKey: "key"}).Error)
	response = passkeyDomainRequest(t, "/api/option/passkey/domains", request, identity, "https://example.com", UpdatePasskeyDomains)
	assert.Equal(t, http.StatusConflict, response.Code)
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.NoError(t, common.Unmarshal(result.Data, &preview))
	assert.EqualValues(t, 3, preview.UnknownCredentials)
	options, err = model.AllOption()
	require.NoError(t, err)
	assert.Empty(t, options)
	request["removal_confirmation"] = preview.RemovalConfirmation
	response = passkeyDomainRequest(t, "/api/option/passkey/domains", request, identity, "https://example.com", UpdatePasskeyDomains)
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, response.Body.String())
	assert.Equal(t, upper, settings.LegacyRPIDs)
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&audits).Error)
	for _, audit := range audits {
		if audit.Action == "option.passkey_domains_blocked" {
			assert.False(t, audit.Success)
			assert.Equal(t, http.StatusConflict, audit.Status)
		}
		if audit.Action == "option.passkey_domains_confirmed" {
			assert.True(t, audit.Success)
			require.NotNil(t, audit.Other.Op)
			params, err := common.Marshal(audit.Other.Op.Params)
			require.NoError(t, err)
			var impact struct {
				Known, Unknown int64
				Confirmed      bool
				RemovedRPIDs   []string `json:"removed_rp_ids"`
			}
			require.NoError(t, common.Unmarshal(params, &impact))
			assert.EqualValues(t, 1, impact.Known)
			assert.EqualValues(t, 3, impact.Unknown)
			assert.True(t, impact.Confirmed)
			assert.Equal(t, []string{lower}, impact.RemovedRPIDs)
		}
	}
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "option.passkey_domains_confirmed")
	assert.Contains(t, string(encoded), "option.passkey_domains_blocked")
	assert.NotContains(t, string(encoded), preview.RemovalConfirmation)
	// Simulate an old node's cache after removal: persistence must use DB trust.
	settings.LegacyRPIDs = lower + "," + upper
	assert.ErrorIs(t, model.UpdatePasskeyAssertionState(user.Id, &webauthn.Credential{ID: []byte("0")}, time.Now(), lower), system_setting.ErrPasskeyRPIDUnavailable)
	assert.ErrorIs(t, model.RegisterPasskeyForSession(identity, &model.PasskeyCredential{UserID: user.Id, RPID: &lower, CredentialID: "replacement", PublicKey: "key"}), system_setting.ErrPasskeyRPIDUnavailable)
}

func TestPasskeyDomainSettingsAcceptExactHTTPSInternalHosts(t *testing.T) {
	for _, test := range []struct {
		rpID, origins string
		valid         bool
	}{
		{"intranet", "https://intranet:8443", true},
		{"intranet", "https://child.intranet", false},
		{"intranet", "http://intranet", false},
		{"com", "https://com", false},
		{"127.0.0.1", "https://127.0.0.1", false},
		{"intranet:8443", "https://intranet:8443", false},
		{"localhost", "http://localhost:3000", true},
	} {
		t.Run(test.rpID+"/"+test.origins, func(t *testing.T) {
			setupSecurityEnrollmentTest(t)
			setupPasskeyDomainOptions(t)
			change, err := model.UpdatePasskeyDomainOptions(map[string]string{"passkey.rp_id": test.rpID, "passkey.legacy_rp_ids": "", "passkey.origins": test.origins}, false, "")
			if !test.valid {
				assert.ErrorIs(t, err, system_setting.ErrPasskeyRPIDInvalid)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.rpID, change.RPID)
			assert.Equal(t, "example.com", change.LegacyRPIDs)
		})
	}
}

func TestPasskeyImplicitDomainChangesRetainPreviousTrust(t *testing.T) {
	setupSecurityEnrollmentTest(t)
	setupPasskeyDomainOptions(t)
	settings := system_setting.GetPasskeySettings()
	settings.RPID, settings.Origins = "", ""
	system_setting.ServerAddress = "https://www.example.com"
	require.NoError(t, model.UpdateOption("ServerAddress", "https://example.com"))
	assert.Equal(t, []string{"example.com", "www.example.com"}, system_setting.PasskeySettingsSnapshot().RelyingPartyIDs())
	assert.Equal(t, "https://www.example.com,https://example.com", settings.Origins)
	// Moving the retained RP into the primary is not a removal.
	change, err := model.UpdatePasskeyDomainOptions(map[string]string{"passkey.rp_id": "www.example.com", "passkey.legacy_rp_ids": ""}, false, "")
	require.NoError(t, err)
	assert.Empty(t, change.RemovedRPIDs)
	assert.Equal(t, "example.com", change.LegacyRPIDs)
}

func TestPasskeyRegistrationAndDomainRemovalSerialize(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	setupPasskeyDomainOptions(t)
	system_setting.GetPasskeySettings().Origins = "https://example.com,https://www.example.com"
	require.NoError(t, model.UpdateOption("passkey.legacy_rp_ids", "www.example.com"))
	registering, release := make(chan struct{}), make(chan struct{})
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register("passkey_registration_barrier", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*model.PasskeyCredential); ok {
			close(registering)
			<-release
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Create().Remove("passkey_registration_barrier") })
	rpID := "www.example.com"
	registered, removed := make(chan error, 1), make(chan error, 1)
	go func() {
		registered <- model.RegisterPasskeyForSession(identity, &model.PasskeyCredential{UserID: user.Id, RPID: &rpID, CredentialID: "concurrent", PublicKey: "key"})
	}()
	<-registering
	removing := make(chan struct{})
	go func() {
		close(removing)
		removed <- model.UpdateOption("passkey.legacy_rp_ids", "")
	}()
	<-removing
	// A concurrent snapshot must remain read-only while the write is pending.
	assert.Equal(t, []string{"example.com", "www.example.com"}, system_setting.PasskeySettingsSnapshot().RelyingPartyIDs())
	close(release)
	require.NoError(t, <-registered)
	assert.ErrorIs(t, <-removed, model.ErrPasskeyDomainRemovalConfirmation)
	stored, err := model.GetPasskeyByUserID(user.Id)
	require.NoError(t, err)
	require.NotNil(t, stored.RPID)
	assert.Equal(t, rpID, *stored.RPID)
}

func TestPasskeyDomainEndpointRequiresRoot(t *testing.T) {
	user, _ := setupSecurityEnrollmentTest(t)
	setupPasskeyDomainOptions(t)
	bundle, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "domain-settings-test")
	require.NoError(t, err)
	router := gin.New()
	router.PUT("/api/option/passkey/domains", middleware.RootAuth(), UpdatePasskeyDomains)
	for _, bearer := range []string{"", bundle.AccessToken} {
		request := httptest.NewRequest(http.MethodPut, "/api/option/passkey/domains", strings.NewReader(`{"rp_id":"example.com","legacy_rp_ids":"","origins":"https://example.com","preview":true}`))
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, response.Code)
	}
	options, err := model.AllOption()
	require.NoError(t, err)
	assert.Empty(t, options)
}

func TestPasskeyDefaultsDoNotRestrictUnrelatedServerAddressSetup(t *testing.T) {
	setupSecurityEnrollmentTest(t)
	setupPasskeyDomainOptions(t)
	settings := system_setting.GetPasskeySettings()
	settings.Enabled, settings.RPID, settings.LegacyRPIDs, settings.Origins = false, "", "", ""
	system_setting.ServerAddress = ""
	require.NoError(t, model.UpdateOption("ServerAddress", "http://127.0.0.1:3000"))
	assert.Equal(t, "http://127.0.0.1:3000", system_setting.ServerAddress)
	assert.Empty(t, settings.RPID)
	assert.Empty(t, settings.LegacyRPIDs)
}

func TestPasskeyRegistrationRejectsUnavailableRequestOrigin(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	settings := system_setting.GetPasskeySettings()
	settings.RPID = "www.example.com"
	settings.LegacyRPIDs = "example.com"
	settings.Origins = "https://example.com,https://www.example.com"
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: "passkey.register"}, "password")
	response := securityEnrollmentRequest(http.MethodPost, "/api/user/passkey/register/begin", "", proof, identity, func(c *gin.Context) {
		c.Request.Header.Set("Origin", "https://example.com")
		PasskeyRegisterBegin(c)
	})
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	assert.False(t, result.Success)
	assert.Equal(t, "PASSKEY_RP_ID_UNAVAILABLE", result.Code)
	var count int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposePasskeyRegister).Count(&count).Error)
	assert.Zero(t, count)
}

func TestPasskeyRPIDRotationRetainsDefaultsAndRollsBackOnFailure(t *testing.T) {
	setupSecurityEnrollmentTest(t)
	setupPasskeyDomainOptions(t)
	settings := system_setting.GetPasskeySettings()
	settings.RPID, settings.LegacyRPIDs = "", ""
	settings.Origins = "https://www.example.com,https://example.com,https://login.example.com"
	system_setting.ServerAddress = "https://www.example.com"
	require.NoError(t, model.DB.Create(&model.Option{Key: "passkey.rp_id", Value: ""}).Error)
	require.NoError(t, model.UpdateOption("passkey.rp_id", "example.com"))
	assert.Equal(t, []string{"example.com", "www.example.com"}, system_setting.PasskeySettingsSnapshot().RelyingPartyIDs())
	// Simulate a second node with an older local cache. Persisted domains win.
	system_setting.GetPasskeySettings().RPID = "www.example.com"
	require.NoError(t, model.UpdateOption("passkey.rp_id", "login.example.com"))
	assert.Equal(t, []string{"login.example.com", "www.example.com", "example.com"}, system_setting.PasskeySettingsSnapshot().RelyingPartyIDs())
	before, err := model.AllOption()
	require.NoError(t, err)
	snapshot := system_setting.PasskeySettingsSnapshot()
	failure := errors.New("synthetic option write failure")
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("passkey_option_failure", func(tx *gorm.DB) {
		if option, ok := tx.Statement.Dest.(*model.Option); ok && option.Key == "passkey.legacy_rp_ids" {
			tx.AddError(failure)
		}
	}))
	assert.ErrorIs(t, model.UpdateOptionsBulk(map[string]string{"passkey.rp_id": "next.example.com", "passkey.legacy_rp_ids": "www.example.com"}), failure)
	require.NoError(t, model.DB.Callback().Update().Remove("passkey_option_failure"))
	after, err := model.AllOption()
	require.NoError(t, err)
	assert.ElementsMatch(t, before, after)
	assert.Equal(t, snapshot, system_setting.PasskeySettingsSnapshot())
	for _, invalid := range []string{"*.example.com", "https://example.com", "example.com:443", "example.com/path", "com"} {
		assert.ErrorIs(t, model.UpdateOption("passkey.rp_id", invalid), system_setting.ErrPasskeyRPIDInvalid)
		assert.ErrorIs(t, model.UpdateOption("passkey.legacy_rp_ids", invalid), system_setting.ErrPasskeyRPIDInvalid)
	}
	assert.Equal(t, snapshot, system_setting.PasskeySettingsSnapshot())
	// Older settings accepted mixed case. Rotation must preserve its hash input.
	require.NoError(t, model.DB.Model(&model.Option{}).Where(&model.Option{Key: "passkey.rp_id"}).Update("value", "WWW.example.com").Error)
	require.NoError(t, model.UpdateOption("passkey.rp_id", "next.example.com"))
	assert.Contains(t, system_setting.PasskeySettingsSnapshot().RelyingPartyIDs(), "WWW.example.com")
}

func TestPasskeyRPIDRotationKeepsInFlightRegistration(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	setupPasskeyDomainOptions(t)
	system_setting.GetPasskeySettings().Origins = "https://example.com,https://www.example.com"
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: "passkey.register"}, "password")
	begin := decodePasskeyDomainBegin(t, securityEnrollmentRequest(http.MethodPost, "/api/user/passkey/register/begin", "", proof, identity, PasskeyRegisterBegin))
	assert.Equal(t, "example.com", begin.Options.PublicKey.RP.ID)
	require.NoError(t, model.UpdateOption("passkey.rp_id", "www.example.com"))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	response := passkeyDomainRequest(t, "/api/user/passkey/register/finish", passkeyFinishRequest{FlowToken: begin.FlowToken, Credential: securityPasskeyResponse(t, key, begin.Options.PublicKey.Challenge, true, 0)}, identity, "https://example.com", PasskeyRegisterFinish)
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, response.Body.String())
	credential, err := model.GetPasskeyByUserID(user.Id)
	require.NoError(t, err)
	require.NotNil(t, credential.RPID)
	assert.Equal(t, "example.com", *credential.RPID)
	// A later registration uses the current primary, not the retained domain.
	wa, err := passkeysvc.BuildWebAuthn(httptest.NewRequest(http.MethodPost, "https://www.example.com/api/user/passkey/register/begin", nil))
	require.NoError(t, err)
	options, _, err := wa.BeginRegistration(passkeysvc.NewWebAuthnUser(user, credential))
	require.NoError(t, err)
	assert.Equal(t, "www.example.com", options.Response.RelyingParty.ID)
}

func TestPasskeyDomainFailuresDoNotLogCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &output
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previousWriter
		common.LogWriterMu.Unlock()
	})
	response := securityEnrollmentRequest(http.MethodPost, "/api/user/passkey/login/finish", "", "", service.AuthIdentity{}, func(c *gin.Context) {
		c.Set("passkey_rp_id", "www.example.com")
		writeSecurityOperationError(c, protocol.ErrVerification.WithDetails("private-flow-token").WithInfo("private-challenge"))
	})
	assert.Contains(t, response.Body.String(), "SECURITY_VERIFICATION_FAILED")
	assert.Contains(t, output.String(), "verification_error")
	assert.Contains(t, output.String(), "www.example.com")
	for _, secret := range []string{"private-flow-token", "private-challenge"} {
		assert.NotContains(t, response.Body.String(), secret)
		assert.NotContains(t, output.String(), secret)
	}
}

func TestPasskeyDomainErrorsRespectRequestLanguage(t *testing.T) {
	user, _ := setupSecurityEnrollmentTest(t)
	setupPasskeyDomainOptions(t)
	system_setting.GetPasskeySettings().LegacyRPIDs = "www.example.com"
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id, CredentialID: "unknown-domain", PublicKey: "key"}).Error)
	for _, locale := range []struct {
		language, invalid, unavailable, removal string
	}{
		{"zh-CN", "通行密钥域名无效。请填写域名，不包含协议、端口、路径或通配符。", "此通行密钥域名无法在当前网站使用。请前往原网站或选择其他验证方式。", "请核对受影响的通行密钥并确认删除域名。配置或影响范围发生变化后，需要重新确认。"},
		{"zh-TW", "通行金鑰網域無效。請填寫網域，不包含通訊協定、連接埠、路徑或萬用字元。", "此通行金鑰網域無法在目前網站使用。請前往原網站或選擇其他驗證方式。", "請核對受影響的通行金鑰並確認刪除網域。設定或影響範圍變更後，需要重新確認。"},
		{"en", "Invalid Passkey domain. Enter a domain without a scheme, port, path or wildcard.", "This Passkey domain is not available on this website. Use its original website or another verification method.", "Review the affected Passkeys and confirm the domain removal. If the settings or impact have changed, confirmation is required again."},
	} {
		t.Run(locale.language, func(t *testing.T) {
			for _, request := range []struct {
				path, body, code, message string
				handler                   gin.HandlerFunc
			}{
				{"/api/option/", `{"key":"passkey.rp_id","value":"localhost:3000"}`, "PASSKEY_RP_ID_INVALID", locale.invalid, UpdateOption},
				{"/api/option/", `{"key":"passkey.legacy_rp_ids","value":"localhost:3001"}`, "PASSKEY_RP_ID_INVALID", locale.invalid, UpdateOption},
				{"/api/option/", `{"key":"passkey.legacy_rp_ids","value":""}`, "PASSKEY_RP_ID_REMOVAL_CONFIRMATION_REQUIRED", locale.removal, UpdateOption},
				{"/api/user/passkey/login/begin", `{"rp_id":"unconfigured.example.com"}`, "PASSKEY_RP_ID_UNAVAILABLE", locale.unavailable, PasskeyLoginBegin},
			} {
				response := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(response)
				method := http.MethodPost
				if request.path == "/api/option/" {
					method = http.MethodPut
				}
				c.Request = httptest.NewRequest(method, request.path, strings.NewReader(request.body))
				c.Request.Header.Set("Accept-Language", locale.language)
				c.Request.Header.Set("Origin", "https://example.com")
				c.Set("id", user.Id)
				c.Set("role", common.RoleRootUser)
				request.handler(c)
				var result securityEnrollmentResponse
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
				expectedStatus := http.StatusOK
				if request.code == "PASSKEY_RP_ID_REMOVAL_CONFIRMATION_REQUIRED" {
					expectedStatus = http.StatusConflict
				}
				assert.Equal(t, expectedStatus, response.Code)
				assert.False(t, result.Success)
				assert.Equal(t, request.code, result.Code)
				assert.Equal(t, request.message, result.Message)
			}
		})
	}
}

func TestPasskeyRPIDMigrationPreservesExistingCredentials(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprintf("upgrade=%t", upgrade), func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			key := newSecurityLoginPasskey(t, user.Id)
			before, err := model.GetPasskeyByUserID(user.Id)
			require.NoError(t, err)
			if upgrade {
				// This schema is copied from the latest released model at
				// v1.0.0-rc.36, with the same GORM and database driver versions.
				require.NoError(t, model.DB.Migrator().DropTable(&model.PasskeyCredential{}))
				require.NoError(t, model.DB.AutoMigrate(&passkeyCredentialBeforeRPID{}))
				encoded, err := common.Marshal(before)
				require.NoError(t, err)
				var legacy passkeyCredentialBeforeRPID
				require.NoError(t, common.Unmarshal(encoded, &legacy))
				require.NoError(t, model.DB.Create(&legacy).Error)
			}
			pool, err := model.DB.DB()
			require.NoError(t, err)
			for range 2 {
				// Each migration represents a new application startup. Discard
				// PostgreSQL's old SELECT * prepared plans along with its old pool.
				pool.SetMaxIdleConns(0)
				pool.SetMaxIdleConns(2)
				require.NoError(t, model.DB.AutoMigrate(&model.PasskeyCredential{}))
			}
			after, err := model.GetPasskeyByUserID(user.Id)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.True(t, model.DB.Migrator().HasIndex(&model.PasskeyCredential{}, "idx_passkey_credentials_deleted_at"))
			assert.Error(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id, CredentialID: "another-key", PublicKey: "key"}).Error)
			assert.Error(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id + 1, CredentialID: before.CredentialID, PublicKey: "key"}).Error)
			system_setting.GetPasskeySettings().LegacyRPIDs = "www.example.com"
			system_setting.GetPasskeySettings().Origins = "https://example.com,https://www.example.com"
			begin := decodePasskeyDomainBegin(t, passkeyDomainRequest(t, "/api/user/passkey/login/begin", map[string]string{"rp_id": "www.example.com"}, identity, "https://www.example.com", PasskeyLoginBegin))
			response := passkeyDomainRequest(t, "/api/user/passkey/login/finish", map[string]any{"flow_token": begin.FlowToken, "credential": passkeyDomainAssertion(t, key, begin.Options.PublicKey.Challenge, "www.example.com", "https://www.example.com", user.Id, true)}, identity, "https://www.example.com", PasskeyLoginFinish)
			var result securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.True(t, result.Success, response.Body.String())
		})
	}
}

func (*passkeyTestBody) Close() error { return nil }

func TestParsePasskeyFinishRequestDoesNotRewriteRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bodyText := `{"flow_token":"flow-1","credential":{"id":"credential-1"}}`
	body := &passkeyTestBody{Reader: strings.NewReader(bodyText)}
	request := httptest.NewRequest(http.MethodPost, "/api/user/passkey/register/finish", nil)
	request.Body = body
	request.ContentLength = int64(len(bodyText))
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	parsed, err := parsePasskeyFinishRequest(context)
	require.NoError(t, err)
	assert.Equal(t, "flow-1", parsed.FlowToken)
	assert.JSONEq(t, `{"id":"credential-1"}`, string(parsed.Credential))
	assert.Same(t, body, context.Request.Body)
	assert.Equal(t, int64(len(bodyText)), context.Request.ContentLength)
}

func TestPasskeyRegisterFinishRejectsUnapprovedFlowWithoutConsumingIt(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	system_setting.GetPasskeySettings().UserVerification = "required"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	payload, err := common.Marshal(map[string]any{"scope": service.VerificationScopePasskeyRegister})
	require.NoError(t, err)
	token, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposePasskeyRegister, UserId: identity.UserID, SessionId: identity.SessionID,
		Payload: string(payload), ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	body, err := common.Marshal(passkeyFinishRequest{
		FlowToken: token, Credential: securityPasskeyResponse(t, key, "test-challenge", true, 0),
	})
	require.NoError(t, err)
	response := securityEnrollmentRequest("POST", "/api/user/passkey/register/finish", string(body), "", identity, PasskeyRegisterFinish)
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	assert.False(t, result.Success)
	assert.Equal(t, "AUTH_FLOW_INVALID", result.Code)
	_, err = model.GetPasskeyByUserID(identity.UserID)
	assert.ErrorIs(t, err, model.ErrPasskeyNotFound)
	flow, err := model.GetAuthFlow(token, model.AuthFlowMatch{Purpose: model.AuthFlowPurposePasskeyRegister})
	require.NoError(t, err)
	assert.Nil(t, flow.ConsumedAt)
}

type passkeyCredentialBeforeRPID struct {
	ID              int            `json:"id" gorm:"primaryKey"`
	UserID          int            `json:"user_id" gorm:"uniqueIndex;not null"`
	CredentialID    string         `json:"credential_id" gorm:"type:varchar(512);uniqueIndex;not null"` // base64 encoded
	PublicKey       string         `json:"public_key" gorm:"type:text;not null"`                        // base64 encoded
	AttestationType string         `json:"attestation_type" gorm:"type:varchar(255)"`
	AAGUID          string         `json:"aaguid" gorm:"type:varchar(512)"` // base64 encoded
	SignCount       uint32         `json:"sign_count" gorm:"default:0"`
	CloneWarning    bool           `json:"clone_warning"`
	UserPresent     bool           `json:"user_present"`
	UserVerified    bool           `json:"user_verified"`
	BackupEligible  bool           `json:"backup_eligible"`
	BackupState     bool           `json:"backup_state"`
	Transports      string         `json:"transports" gorm:"type:text"`
	Attachment      string         `json:"attachment" gorm:"type:varchar(32)"`
	LastUsedAt      *time.Time     `json:"last_used_at"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"-" gorm:"index"`
}

func (passkeyCredentialBeforeRPID) TableName() string { return "passkey_credentials" }
