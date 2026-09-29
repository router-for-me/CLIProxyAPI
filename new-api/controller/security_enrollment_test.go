package controller

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/fxamacker/cbor/v2"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupSecurityEnrollmentTest(t *testing.T) (*model.User, service.AuthIdentity) {
	t.Helper()
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousSecret := common.RedisEnabled, common.SessionSecret
	previousEncryption := common.PasswordLoginEncryptionEnabled
	previousSettings := *system_setting.GetPasskeySettings()
	dialect := os.Getenv("TEST_SECURITY_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
	db, _ := newAuditTestDatabase(t, dialect, dsn)
	logDB, _ := newAuditTestDatabase(t, dialect, dsn)
	db.Logger = logger.Default.LogMode(logger.Silent)
	logDB.Logger = logger.Default.LogMode(logger.Silent)
	versionQuery := "SELECT VERSION()"
	if dialect == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database: %s %s", dialect, version)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.TwoFABackupCode{}, &model.PasskeyCredential{}, &model.AuthFlow{}, &model.UserOAuthBinding{}, &model.Option{}))
	require.NoError(t, logDB.AutoMigrate(&model.AuditLog{}))
	model.DB, model.LOG_DB = db, logDB
	dbType := common.DatabaseTypeSQLite
	if dialect == "mysql" {
		dbType = common.DatabaseTypeMySQL
	}
	if dialect == "postgres" {
		dbType = common.DatabaseTypePostgreSQL
	}
	common.SetDatabaseTypes(dbType, dbType)
	common.PasswordLoginEncryptionEnabled = false
	common.RedisEnabled = false
	common.SessionSecret = "security-enrollment-test-secret"
	*system_setting.GetPasskeySettings() = system_setting.PasskeySettings{Enabled: true, RPID: "example.com", Origins: "https://example.com", RPDisplayName: "new-api"}
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.SessionSecret = previousRedis, previousSecret
		common.PasswordLoginEncryptionEnabled = previousEncryption
		*system_setting.GetPasskeySettings() = previousSettings
		connection, err := db.DB()
		if err == nil {
			_ = connection.Close()
		}
	})
	password, err := common.Password2Hash("enrollment-password")
	require.NoError(t, err)
	user := &model.User{Username: "enrollment-user", Password: password, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	bundle, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "enrollment-test")
	require.NoError(t, err)
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	return user, identity
}

func securityEnrollmentRequest(method, path, body, proof string, identity service.AuthIdentity, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Security-Proof", proof)
	c.Set("id", identity.UserID)
	c.Set("role", common.RoleCommonUser)
	c.Set("session_id", identity.SessionID)
	c.Set("auth_version", identity.UserAuthVersion)
	c.Set("session_version", identity.SessionVersion)
	handler(c)
	return response
}

func issueSecurityEnrollmentProof(t *testing.T, identity service.AuthIdentity, operation service.VerificationOperation, method string) string {
	t.Helper()
	binding, err := service.BindVerificationOperation(operation)
	require.NoError(t, err)
	proof, _, err := service.IssueSecurityProof(identity, method, binding)
	require.NoError(t, err)
	return proof
}

func TestSecurityEnrollmentAccessTokenRequiresProofBeforeMutation(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.UpdateUserAccessToken(user.Id, "existing-system-token"))
	for _, endpoint := range []struct {
		method  string
		handler gin.HandlerFunc
	}{
		{"GET", GenerateAccessToken},
		{"POST", GenerateAccessToken},
		{"DELETE", RevokeAccessToken},
	} {
		t.Run(endpoint.method, func(t *testing.T) {
			response := securityEnrollmentRequest(endpoint.method, "/api/user/token", "", "", identity, endpoint.handler)
			var body securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, http.StatusForbidden, response.Code)
			assert.False(t, body.Success)
			assert.Equal(t, "SECURITY_PROOF_REQUIRED", body.Code)
			stored, err := model.ValidateAccessToken("existing-system-token")
			require.NoError(t, err)
			require.NotNil(t, stored)
			assert.Equal(t, user.Id, stored.Id)
		})
	}
}

func TestSecurityEnrollmentAccessTokenMethodPolicy(t *testing.T) {
	for _, test := range []struct {
		name, method                     string
		password, passkey, twoFA, locked bool
		disabledPasskey, oauth, wechat   bool
		available                        bool
	}{
		{name: "password", method: "password", password: true, oauth: true, available: true},
		{name: "existing passkey", method: "passkey", password: true, passkey: true, available: true},
		{name: "existing twofa", method: "2fa", password: true, passkey: true, twoFA: true, available: true},
		{name: "locked twofa blocks fallback", method: "2fa", password: true, twoFA: true, locked: true},
		{name: "disabled passkey blocks fallback", method: "passkey", password: true, passkey: true, disabledPasskey: true},
		{name: "disabled passkey does not block password", method: "password", password: true, disabledPasskey: true, available: true},
		{name: "linked oauth", method: "oauth", oauth: true, available: true},
		{name: "wechat session cannot manage tokens", method: "oauth", wechat: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			if !test.password {
				require.NoError(t, model.DB.Model(user).Update("password", "").Error)
			}
			if test.passkey {
				require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id, CredentialID: "existing-key", PublicKey: "public-key"}).Error)
			}
			if test.twoFA {
				twoFA := &model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}
				if test.locked {
					until := time.Now().Add(time.Minute)
					twoFA.LockedUntil = &until
				}
				require.NoError(t, model.DB.Create(twoFA).Error)
			}
			if test.oauth {
				require.NoError(t, model.DB.Model(user).Update("github_id", "linked-user").Error)
				oauth.Register("access-token-oauth", &enrollmentOAuthProvider{externalID: "linked-user"})
				t.Cleanup(func() { oauth.Unregister("access-token-oauth") })
			}
			if test.wechat {
				require.NoError(t, model.DB.Model(user).Update("wechat_id", "wechat-user").Error)
			}
			system_setting.GetPasskeySettings().Enabled = !test.disabledPasskey
			passwordScope := service.VerificationScopePasswordChange
			if !test.password {
				passwordScope = service.VerificationScopePasswordSet
			}
			for _, scope := range []string{service.VerificationScopeAccessTokenGenerate, service.VerificationScopeAccessTokenRevoke,
				service.VerificationScopeAccountBind, service.VerificationScopeAccountUnbind, passwordScope} {
				requirements, err := service.GetVerificationRequirements(identity, scope)
				require.NoError(t, err)
				count := 1
				if test.twoFA && test.passkey {
					count = 2
				}
				require.Len(t, requirements.Methods, count)
				assert.Equal(t, test.method, requirements.Methods[0].Method)
				assert.Equal(t, test.available, requirements.Methods[0].Available)
				if count == 2 {
					assert.Equal(t, service.VerificationMethodOption{Method: "passkey", Available: true}, requirements.Methods[1])
				}
				if test.wechat {
					input := service.VerificationInput{Scope: scope, Method: "session"}
					switch scope {
					case service.VerificationScopeAccountBind:
						input.Context = []byte(`{"provider":"email","email":"new@example.com"}`)
					case service.VerificationScopeAccountUnbind:
						input.Context = []byte(`{"provider_id":1}`)
					}
					_, err := service.VerifySecurityInput(identity, input)
					assert.ErrorIs(t, err, service.ErrProofMethod)
				}
			}
		})
	}
}

func TestSecurityEnrollmentAccessTokenLifecycleConsumesProofs(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.UpdateUserAccessToken(user.Id, "previous-token"))
	previousToken := "previous-token"
	for _, method := range []string{"GET", "POST"} {
		proof, err := service.VerifySecurityInput(identity, service.VerificationInput{
			Scope: service.VerificationScopeAccessTokenGenerate, Method: "password", Password: "enrollment-password",
		})
		require.NoError(t, err)
		wrongScope := securityEnrollmentRequest("DELETE", "/api/user/token", "", proof.ProofToken, identity, RevokeAccessToken)
		assert.Contains(t, wrongScope.Body.String(), `"code":"SECURITY_PROOF_SCOPE_MISMATCH"`)
		response := securityEnrollmentRequest(method, "/api/user/token", "", proof.ProofToken, identity, GenerateAccessToken)
		var body securityEnrollmentResponse
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
		require.True(t, body.Success, body.Message)
		var token string
		require.NoError(t, common.Unmarshal(body.Data, &token))
		assert.GreaterOrEqual(t, len(token), 28)
		assert.LessOrEqual(t, len(token), 32)
		assert.NotEqual(t, previousToken, token)
		stored, err := model.ValidateAccessToken(token)
		require.NoError(t, err)
		require.NotNil(t, stored)
		assert.Equal(t, user.Id, stored.Id)
		assert.Equal(t, model.AccessTokenFingerprint(token), model.AccessTokenFingerprint(stored.GetAccessToken()))
		oldUser, err := model.ValidateAccessToken(previousToken)
		assert.Nil(t, oldUser)
		require.NoError(t, err)
		response = securityEnrollmentRequest(method, "/api/user/token", "", proof.ProofToken, identity, GenerateAccessToken)
		assert.Contains(t, response.Body.String(), `"code":"SECURITY_PROOF_CONSUMED"`)
		previousToken = token
	}
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeAccessTokenRevoke}, "password")
	response := securityEnrollmentRequest("DELETE", "/api/user/token", "", proof, identity, RevokeAccessToken)
	var body securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	stored, err := model.GetUserById(user.Id, true)
	require.NoError(t, err)
	assert.Empty(t, stored.GetAccessToken())
	revokedUser, err := model.ValidateAccessToken(previousToken)
	require.NoError(t, err)
	assert.Nil(t, revokedUser)
	response = securityEnrollmentRequest("DELETE", "/api/user/token", "", proof, identity, RevokeAccessToken)
	assert.Contains(t, response.Body.String(), `"code":"SECURITY_PROOF_CONSUMED"`)
	response = securityEnrollmentRequest("GET", "/api/user/token/status", "", "", identity, GetAccessTokenStatus)
	assert.Contains(t, response.Body.String(), `"exists":false`)
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&audits).Error)
	require.Len(t, audits, 3)
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "access_token.generate")
	assert.Contains(t, string(encoded), "access_token.revoke")
	assert.NotContains(t, string(encoded), previousToken)
	assert.NotContains(t, string(encoded), proof)
}

func TestSecurityEnrollmentAccessTokenRejectsInvalidProofs(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.UpdateUserAccessToken(user.Id, "unchanged-token"))
	for _, endpoint := range []struct {
		method, scope string
		handler       gin.HandlerFunc
	}{
		{"GET", service.VerificationScopeAccessTokenGenerate, GenerateAccessToken},
		{"POST", service.VerificationScopeAccessTokenGenerate, GenerateAccessToken},
		{"DELETE", service.VerificationScopeAccessTokenRevoke, RevokeAccessToken},
	} {
		for _, failure := range []string{"session", "user", "expired"} {
			t.Run(endpoint.method+"/"+failure, func(t *testing.T) {
				proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: endpoint.scope}, "password")
				requestIdentity := identity
				code := "SECURITY_PROOF_INVALID"
				switch failure {
				case "session":
					requestIdentity.SessionID = "other-session"
				case "user":
					requestIdentity.UserID++
				case "expired":
					require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposeSecurityProof).Update("expires_at", time.Now().Add(-time.Minute)).Error)
					code = "SECURITY_PROOF_EXPIRED"
				}
				response := securityEnrollmentRequest(endpoint.method, "/api/user/token", "", proof, requestIdentity, endpoint.handler)
				assert.Equal(t, http.StatusForbidden, response.Code)
				assert.Contains(t, response.Body.String(), code)
				stored, err := model.ValidateAccessToken("unchanged-token")
				require.NoError(t, err)
				require.NotNil(t, stored)
				assert.Equal(t, user.Id, stored.Id)
			})
		}
	}
}

func TestSecurityEnrollmentAccessTokenFailureDoesNotRestoreProof(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.UpdateUserAccessToken(user.Id, "unchanged-token"))
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("access_token_write_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("private database failure"))
		}
	}))
	for _, endpoint := range []struct {
		method, scope string
		handler       gin.HandlerFunc
	}{
		{"POST", service.VerificationScopeAccessTokenGenerate, GenerateAccessToken},
		{"DELETE", service.VerificationScopeAccessTokenRevoke, RevokeAccessToken},
	} {
		proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: endpoint.scope}, "password")
		response := securityEnrollmentRequest(endpoint.method, "/api/user/token", "", proof, identity, endpoint.handler)
		assert.Equal(t, http.StatusInternalServerError, response.Code)
		assert.NotContains(t, response.Body.String(), "private database")
		response = securityEnrollmentRequest(endpoint.method, "/api/user/token", "", proof, identity, endpoint.handler)
		assert.Contains(t, response.Body.String(), `"code":"SECURITY_PROOF_CONSUMED"`)
	}
	stored, err := model.ValidateAccessToken("unchanged-token")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, user.Id, stored.Id)
}

func authorizeSecurityEnrollment(t *testing.T, identity service.AuthIdentity) *model.AuthFlowAuthorization {
	t.Helper()
	operation := service.VerificationOperation{Scope: service.VerificationScopeTwoFASetup}
	proof := issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
	authorization, err := service.ConsumeOperationProof(proof, identity, operation)
	require.NoError(t, err)
	return authorization
}

// securityPasskeyResponse acts as a software authenticator at the browser boundary.
// The handlers still validate the real WebAuthn challenge, origin and signature.
func securityPasskeyResponse(t *testing.T, key *ecdsa.PrivateKey, challenge string, registration bool, counter uint32, userVerified ...bool) json.RawMessage {
	t.Helper()
	ceremony := "webauthn.get"
	if registration {
		ceremony = "webauthn.create"
	}
	clientData, err := common.Marshal(map[string]any{"type": ceremony, "challenge": challenge, "origin": "https://example.com"})
	require.NoError(t, err)
	credentialID := sha256.Sum256(elliptic.Marshal(key.Curve, key.X, key.Y))
	rpIDHash := sha256.Sum256([]byte("example.com"))
	authData := append([]byte{}, rpIDHash[:]...)
	response := map[string]any{"clientDataJSON": base64.RawURLEncoding.EncodeToString(clientData)}
	if registration {
		flags := byte(0x45) // user present, user verified, attested credential
		if len(userVerified) > 0 && !userVerified[0] {
			flags = 0x41
		}
		authData = append(authData, flags)
		authData = append(authData, make([]byte, 4+16)...)
		authData = binary.BigEndian.AppendUint16(authData, uint16(len(credentialID)))
		authData = append(authData, credentialID[:]...)
		publicKey, err := cbor.Marshal(map[int]any{
			1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32)),
		})
		require.NoError(t, err)
		authData = append(authData, publicKey...)
		attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "authData": authData, "attStmt": map[string]any{}})
		require.NoError(t, err)
		response["attestationObject"] = base64.RawURLEncoding.EncodeToString(attestation)
	} else {
		flags := byte(0x05) // user present and verified
		if len(userVerified) > 0 && !userVerified[0] {
			flags = 0x01
		}
		authData = append(authData, flags)
		authData = binary.BigEndian.AppendUint32(authData, counter)
		clientHash := sha256.Sum256(clientData)
		signedData := append(append([]byte{}, authData...), clientHash[:]...)
		signedHash := sha256.Sum256(signedData)
		signature, err := ecdsa.SignASN1(rand.Reader, key, signedHash[:])
		require.NoError(t, err)
		response["authenticatorData"] = base64.RawURLEncoding.EncodeToString(authData)
		response["signature"] = base64.RawURLEncoding.EncodeToString(signature)
	}
	id := base64.RawURLEncoding.EncodeToString(credentialID[:])
	credential, err := common.Marshal(map[string]any{"id": id, "rawId": id, "type": "public-key", "response": response})
	require.NoError(t, err)
	return credential
}

func TestSecurityEnrollmentRejectsMissingProofBeforeCreatingCredentials(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	for _, test := range []struct {
		path    string
		handler gin.HandlerFunc
	}{
		{"/api/user/2fa/setup", Setup2FA},
		{"/api/user/passkey/register/begin", PasskeyRegisterBegin},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := securityEnrollmentRequest(http.MethodPost, test.path, `{}`, "", identity, test.handler)
			assert.Equal(t, http.StatusForbidden, response.Code)
			assert.Contains(t, response.Body.String(), "SECURITY_PROOF_REQUIRED")
			assert.NotContains(t, response.Body.String(), "qr_code_data")
		})
	}
	pending, err := model.GetTwoFAByUserId(user.Id)
	require.NoError(t, err)
	assert.Nil(t, pending)
}

type securityEnrollmentResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Code    string          `json:"code"`
	Data    json.RawMessage `json:"data"`
}

func TestSecurityEnrollmentMethodPolicy(t *testing.T) {
	for _, test := range []struct {
		name                                              string
		password, passkey, twoFA, locked, disabledPasskey bool
		method                                            string
		available                                         bool
	}{
		{name: "first factor uses password", password: true, method: "password", available: true},
		{name: "existing passkey takes precedence", password: true, passkey: true, method: "passkey", available: true},
		{name: "twofa and passkey are alternatives", password: true, passkey: true, twoFA: true, method: "2fa", available: true},
		{name: "locked twofa permits passkey", password: true, passkey: true, twoFA: true, locked: true, method: "2fa"},
		{name: "disabled passkey does not fall back", password: true, passkey: true, disabledPasskey: true, method: "passkey"},
		{name: "disabled registration does not request a password", password: true, disabledPasskey: true, method: "password"},
		{name: "passwordless account without providers is unavailable", method: "oauth"},
	} {
		t.Run(test.name, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			if !test.password {
				require.NoError(t, model.DB.Model(user).Update("password", "").Error)
			}
			if test.passkey {
				require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id, CredentialID: "enrolled-passkey", PublicKey: "public-key"}).Error)
			}
			if test.twoFA {
				twoFA := &model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}
				if test.locked {
					until := time.Now().Add(time.Minute)
					twoFA.LockedUntil = &until
				}
				require.NoError(t, model.DB.Create(twoFA).Error)
			}
			system_setting.GetPasskeySettings().Enabled = !test.disabledPasskey
			requirements, err := service.GetVerificationRequirements(identity, "passkey.register")
			require.NoError(t, err)
			count := 1
			if test.twoFA && test.passkey {
				count = 2
			}
			require.Len(t, requirements.Methods, count)
			assert.Equal(t, test.method, requirements.Methods[0].Method)
			assert.Equal(t, test.available, requirements.Methods[0].Available)
			if count == 2 {
				assert.Equal(t, service.VerificationMethodOption{Method: "passkey", Available: true}, requirements.Methods[1])
			}
			if test.passkey && !test.twoFA {
				requirements, err = service.GetVerificationRequirements(identity, "2fa.setup")
				require.NoError(t, err)
				assert.Equal(t, "passkey", requirements.Methods[0].Method)
			}
		})
	}
}

func TestSecurityEnrollmentPasswordProofIsBoundToSessionAndAction(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	response := securityEnrollmentRequest("POST", "/api/verify", `{"method":"password","scope":"2fa.setup","password":"wrong"}`, "", identity, UniversalVerify)
	assert.NotContains(t, response.Body.String(), "proof_token")
	response = securityEnrollmentRequest("POST", "/api/verify", `{"method":"password","scope":"2fa.setup","password":"enrollment-password"}`, "", identity, UniversalVerify)
	var body securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	var proof service.SecurityProof
	require.NoError(t, common.Unmarshal(body.Data, &proof))
	_, err := service.ConsumeOperationProof(proof.ProofToken, identity, service.VerificationOperation{Scope: "passkey.register"})
	assert.ErrorIs(t, err, service.ErrProofScope)
	other := identity
	other.SessionID = "another-session"
	_, err = service.ConsumeOperationProof(proof.ProofToken, other, service.VerificationOperation{Scope: "2fa.setup"})
	assert.ErrorIs(t, err, service.ErrAuthTokenInvalid)
	_, err = service.ConsumeOperationProof(proof.ProofToken, identity, service.VerificationOperation{Scope: "2fa.setup"})
	require.NoError(t, err)
	response = securityEnrollmentRequest("POST", "/api/verify", `{"method":"password","scope":"channel.key.read","password":"enrollment-password"}`, "", identity, UniversalVerify)
	assert.NotContains(t, response.Body.String(), "proof_token")
	common.PasswordLoginEncryptionEnabled = true
	response = securityEnrollmentRequest("POST", "/api/verify", `{"method":"password","scope":"2fa.setup","password":"enrollment-password"}`, "", identity, UniversalVerify)
	assert.NotContains(t, response.Body.String(), "proof_token", "encryption-required mode must reject plaintext")
	common.PasswordLoginEncryptionEnabled = false
	require.NoError(t, model.DB.Model(user).Update("auth_version", 2).Error)
	_, err = service.ConsumeOperationProof(proof.ProofToken, identity, service.VerificationOperation{Scope: "2fa.setup"})
	assert.ErrorIs(t, err, service.ErrLoginSessionRevoked)
}

func TestSecurityEnrollmentProofStartsOnlyOneSetup(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: "2fa.setup"}, "password")
	first := securityEnrollmentRequest("POST", "/api/user/2fa/setup", `{}`, proof, identity, Setup2FA)
	var body securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(first.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	second := securityEnrollmentRequest("POST", "/api/user/2fa/setup", `{}`, proof, identity, Setup2FA)
	assert.Equal(t, http.StatusForbidden, second.Code)
	require.NoError(t, common.Unmarshal(second.Body.Bytes(), &body))
	assert.False(t, body.Success)
	assert.Equal(t, "SECURITY_PROOF_CONSUMED", body.Code)
}

func TestSecurityEnrollmentOperationContext(t *testing.T) {
	for _, test := range []struct {
		name, scope, context string
		err                  error
	}{
		{"channel", "channel.key.read", `{"channel_id":123}`, nil},
		{"missing channel", "channel.key.read", ``, service.ErrVerificationContextInvalid},
		{"null channel", "channel.key.read", `{"channel_id":null}`, service.ErrVerificationContextInvalid},
		{"string channel", "channel.key.read", `{"channel_id":"123"}`, service.ErrVerificationContextInvalid},
		{"fractional channel", "channel.key.read", `{"channel_id":123.5}`, service.ErrVerificationContextInvalid},
		{"zero channel", "channel.key.read", `{"channel_id":0}`, service.ErrVerificationContextInvalid},
		{"negative channel", "channel.key.read", `{"channel_id":-1}`, service.ErrVerificationContextInvalid},
		{"overflow channel", "channel.key.read", `{"channel_id":18446744073709551615}`, service.ErrVerificationContextInvalid},
		{"extra field", "channel.key.read", `{"channel_id":123,"extra":true}`, service.ErrVerificationContextInvalid},
		{"null context", "passkey.register", `null`, service.ErrVerificationContextInvalid},
		{"array context", "passkey.register", `[]`, service.ErrVerificationContextInvalid},
		{"empty enrollment", "passkey.register", `{}`, nil},
		{"implicit enrollment", "passkey.register", ``, nil},
		{"generate access token", "access_token.generate", `{}`, nil},
		{"revoke access token", "access_token.revoke", ``, nil},
		{"access token target injection", "access_token.revoke", `{"user_id":42}`, service.ErrVerificationContextInvalid},
		{"enrollment target injection", "passkey.register", `{"user_id":42}`, service.ErrVerificationContextInvalid},
		{"unknown scope", "user.email.change", `{}`, service.ErrProofScope},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.BindVerificationOperation(service.VerificationOperation{Scope: test.scope, Context: []byte(test.context)})
			assert.ErrorIs(t, err, test.err)
		})
	}
	var first, reordered service.VerificationOperation
	require.NoError(t, common.UnmarshalJsonStr(`{"scope":"channel.key.read","context":{"channel_id":123}}`, &first))
	require.NoError(t, common.UnmarshalJsonStr(`{"context": { "channel_id": 123 }, "scope":"channel.key.read"}`, &reordered))
	firstBinding, err := service.BindVerificationOperation(first)
	require.NoError(t, err)
	secondBinding, err := service.BindVerificationOperation(reordered)
	require.NoError(t, err)
	assert.Equal(t, firstBinding, secondBinding)
}

func TestSecurityEnrollmentChannelProofRejectsMismatchesBeforeConsumption(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	twoFA := &model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}
	require.NoError(t, model.DB.Create(twoFA).Error)
	operation := service.VerificationOperation{Scope: service.VerificationScopeChannelKeyRead, Context: []byte(`{"channel_id":123}`)}
	code, err := totp.GenerateCode(twoFA.Secret, time.Now())
	require.NoError(t, err)
	proof, err := service.VerifySecurityInput(identity, service.VerificationInput{Method: "2fa", Scope: operation.Scope, Context: operation.Context, Code: code})
	require.NoError(t, err)
	for _, test := range []struct {
		name      string
		identity  service.AuthIdentity
		operation service.VerificationOperation
		err       error
	}{
		{"channel", identity, service.VerificationOperation{Scope: operation.Scope, Context: []byte(`{"channel_id":456}`)}, service.ErrProofContext},
		{"scope", identity, service.VerificationOperation{Scope: "passkey.delete"}, service.ErrProofScope},
		{"user", service.AuthIdentity{UserID: user.Id + 1, SessionID: identity.SessionID, UserAuthVersion: identity.UserAuthVersion, SessionVersion: identity.SessionVersion}, operation, service.ErrAuthTokenInvalid},
		{"session", service.AuthIdentity{UserID: user.Id, SessionID: "other-session", UserAuthVersion: identity.UserAuthVersion, SessionVersion: identity.SessionVersion}, operation, service.ErrAuthTokenInvalid},
		{"session version", service.AuthIdentity{UserID: user.Id, SessionID: identity.SessionID, UserAuthVersion: identity.UserAuthVersion, SessionVersion: identity.SessionVersion + 1}, operation, service.ErrAuthTokenInvalid},
		{"user version", service.AuthIdentity{UserID: user.Id, SessionID: identity.SessionID, UserAuthVersion: identity.UserAuthVersion + 1, SessionVersion: identity.SessionVersion}, operation, service.ErrAuthTokenInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.ConsumeOperationProof(proof.ProofToken, test.identity, test.operation)
			assert.ErrorIs(t, err, test.err)
		})
	}
	require.NoError(t, model.DB.Model(twoFA).Update("is_enabled", false).Error)
	_, err = service.ConsumeOperationProof(proof.ProofToken, identity, operation)
	assert.ErrorIs(t, err, service.ErrProofMethod)
	require.NoError(t, model.DB.Model(twoFA).Update("is_enabled", true).Error)
	authorization, err := service.ConsumeOperationProof(proof.ProofToken, identity, operation)
	require.NoError(t, err)
	assert.Positive(t, authorization.ProofID)
	_, err = service.ConsumeOperationProof(proof.ProofToken, identity, operation)
	assert.ErrorIs(t, err, service.ErrProofConsumed)
}

func TestSecurityEnrollmentProofConcurrentConsumption(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	operation := service.VerificationOperation{Scope: "passkey.register"}
	proof := issueSecurityEnrollmentProof(t, identity, operation, "password")
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := service.ConsumeOperationProof(proof, identity, operation)
			results <- err
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err != nil {
			assert.ErrorIs(t, err, service.ErrProofConsumed)
		} else {
			successes++
		}
	}
	assert.Equal(t, 1, successes)
}

func TestSecurityEnrollmentProofRequiresLiveRecordAndExactDeadline(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	operation := service.VerificationOperation{Scope: "passkey.register"}
	proof := issueSecurityEnrollmentProof(t, identity, operation, "password")
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(proof, claims)
	require.NoError(t, err)
	assert.Equal(t, float64(60), claims["exp"].(float64)-claims["iat"].(float64))
	assert.NotEmpty(t, claims["context_hash"])
	assert.NotContains(t, claims, "context")
	assert.NotContains(t, claims, "channel_id")
	var stored model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", model.AuthFlowPurposeSecurityProof).First(&stored).Error)
	assert.Equal(t, int64(claims["exp"].(float64)), stored.ExpiresAt.Unix())
	assert.NotEqual(t, proof, stored.TokenHash)
	assert.NotEqual(t, claims["jti"], stored.TokenHash)
	require.NoError(t, model.DB.Model(&stored).Update("expires_at", time.Now()).Error)
	_, err = service.ConsumeOperationProof(proof, identity, operation)
	assert.ErrorIs(t, err, service.ErrAuthTokenExpired, "database deadline must reject even while the JWT is within its clock tolerance")
	require.NoError(t, model.DB.Delete(&stored).Error)
	_, err = service.ConsumeOperationProof(proof, identity, operation)
	assert.ErrorIs(t, err, service.ErrAuthTokenInvalid)
}

func TestSecurityEnrollmentProofIsBurnedAfterBusinessFailure(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: "passkey.register"}, "password")
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register("security_flow_creation_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "auth_flows" {
			tx.AddError(errors.New("injected creation failure"))
		}
	}))
	response := securityEnrollmentRequest("POST", "/api/user/passkey/register/begin", "", proof, identity, PasskeyRegisterBegin)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	require.NoError(t, model.DB.Callback().Create().Remove("security_flow_creation_failure"))
	response = securityEnrollmentRequest("POST", "/api/user/passkey/register/begin", "", proof, identity, PasskeyRegisterBegin)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "SECURITY_PROOF_CONSUMED")
}

func TestSecurityEnrollmentProofStorageErrorsFailClosed(t *testing.T) {
	for _, stage := range []string{"issuance", "consumption"} {
		t.Run(stage, func(t *testing.T) {
			_, identity := setupSecurityEnrollmentTest(t)
			proof := ""
			if stage == "consumption" {
				proof = issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: "passkey.register"}, "password")
			}
			failure := func(tx *gorm.DB) {
				if tx.Statement.Table == "auth_flows" {
					tx.AddError(errors.New("private proof database failure"))
				}
			}
			if stage == "issuance" {
				require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register("proof_storage_failure", failure))
			} else {
				require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("proof_storage_failure", failure))
			}
			var response *httptest.ResponseRecorder
			if stage == "issuance" {
				response = securityEnrollmentRequest("POST", "/api/verify", `{"method":"password","scope":"passkey.register","password":"enrollment-password"}`, "", identity, UniversalVerify)
				require.NoError(t, model.DB.Callback().Create().Remove("proof_storage_failure"))
			} else {
				response = securityEnrollmentRequest("POST", "/api/user/passkey/register/begin", "", proof, identity, PasskeyRegisterBegin)
				require.NoError(t, model.DB.Callback().Update().Remove("proof_storage_failure"))
			}
			assert.Equal(t, http.StatusInternalServerError, response.Code)
			assert.Contains(t, response.Body.String(), "AUTH_INTERNAL_ERROR")
			assert.NotContains(t, response.Body.String(), "private")
			assert.NotContains(t, response.Body.String(), "proof_token")
			assert.NotContains(t, response.Body.String(), "flow_token")
			var count int64
			require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposePasskeyRegister).Count(&count).Error)
			assert.Zero(t, count)
			if stage == "consumption" {
				response = securityEnrollmentRequest("POST", "/api/user/passkey/register/begin", "", proof, identity, PasskeyRegisterBegin)
				var result securityEnrollmentResponse
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
				assert.True(t, result.Success, "a failed consumption transaction must not burn the proof")
			}
		})
	}
}

func TestSecurityEnrollmentVerificationTransportsRejectInvalidContext(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	oauth.Register("enrollment-oauth", &enrollmentOAuthProvider{externalID: "linked-user"})
	t.Cleanup(func() { oauth.Unregister("enrollment-oauth") })
	for _, test := range []struct {
		path, body string
		handler    gin.HandlerFunc
	}{
		{"/api/verify", `{"method":"password","scope":"passkey.register","context":{"user_id":999},"password":"enrollment-password"}`, UniversalVerify},
		{"/api/user/passkey/verify/begin", `{"scope":"passkey.register","context":{"user_id":999}}`, PasskeyVerifyBegin},
		{"/api/oauth/state", `{"provider":"enrollment-oauth","intent":"verify","scope":"passkey.register","context":{"user_id":999}}`, GenerateOAuthCode},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := securityEnrollmentRequest("POST", test.path, test.body, "", identity, test.handler)
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Contains(t, response.Body.String(), "SECURITY_CONTEXT_INVALID")
		})
	}
	var count int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestSecurityEnrollmentPendingPasskeyRejectsChangedAuthorization(t *testing.T) {
	for _, change := range []string{"expired", "revoked", "session version", "user version", "method", "other session"} {
		t.Run(change, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: "passkey.register"}, "password")
			response := securityEnrollmentRequest("POST", "/api/user/passkey/register/begin", "", proof, identity, PasskeyRegisterBegin)
			var result securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			require.True(t, result.Success, result.Message)
			var begin struct {
				FlowToken string `json:"flow_token"`
				Options   struct {
					PublicKey struct {
						Challenge string `json:"challenge"`
					} `json:"publicKey"`
				} `json:"options"`
			}
			require.NoError(t, common.Unmarshal(result.Data, &begin))
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			require.NoError(t, err)
			body, err := common.Marshal(passkeyFinishRequest{
				FlowToken: begin.FlowToken, Credential: securityPasskeyResponse(t, key, begin.Options.PublicKey.Challenge, true, 0),
			})
			require.NoError(t, err)
			switch change {
			case "expired":
				require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposePasskeyRegister).Update("expires_at", time.Now().Add(-time.Minute)).Error)
			case "revoked":
				_, err = model.RevokeUserSession(user.Id, identity.SessionID, "security-test")
				require.NoError(t, err)
			case "session version":
				require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", identity.SessionID).Update("version", 2).Error)
			case "user version":
				require.NoError(t, model.DB.Model(user).Update("auth_version", 2).Error)
			case "method":
				require.NoError(t, model.DB.Model(user).Update("password", "").Error)
			case "other session":
				other, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "other-session")
				require.NoError(t, err)
				identity, err = service.ParseAccessToken(other.AccessToken)
				require.NoError(t, err)
			}
			response = securityEnrollmentRequest("POST", "/api/user/passkey/register/finish", string(body), "", identity, PasskeyRegisterFinish)
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.False(t, result.Success)
			_, err = model.GetPasskeyByUserID(user.Id)
			assert.ErrorIs(t, err, model.ErrPasskeyNotFound)
		})
	}
}

func TestSecurityEnrollmentPasskeyProofProtectsChannelKeyRead(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}))
	for _, channel := range []model.Channel{
		{Id: 123, Name: "first", Key: "first-channel-secret", Type: 1, Status: 1},
		{Id: 456, Name: "second", Key: "second-channel-secret", Type: 1, Status: 1},
	} {
		require.NoError(t, model.DB.Create(&channel).Error)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	registrationProof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: "passkey.register"}, "password")
	response := securityEnrollmentRequest("POST", "/api/user/passkey/register/begin", "", registrationProof, identity, PasskeyRegisterBegin)
	var body securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	var begin struct {
		FlowToken string `json:"flow_token"`
		Options   struct {
			PublicKey struct {
				Challenge string `json:"challenge"`
			} `json:"publicKey"`
		} `json:"options"`
	}
	require.NoError(t, common.Unmarshal(body.Data, &begin))
	require.NotEmpty(t, begin.Options.PublicKey.Challenge)
	registrationBody, err := common.Marshal(passkeyFinishRequest{
		FlowToken: begin.FlowToken, Credential: securityPasskeyResponse(t, key, begin.Options.PublicKey.Challenge, true, 0),
	})
	require.NoError(t, err)
	// The dedicated registration flow remains authorized after the consumed proof expires.
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposeSecurityProof).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	response = securityEnrollmentRequest("POST", "/api/user/passkey/register/finish", string(registrationBody), "", identity, PasskeyRegisterFinish)
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	var rotation struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, common.Unmarshal(body.Data, &rotation))
	identity, err = service.ParseAccessToken(rotation.AccessToken)
	require.NoError(t, err)
	assert.EqualValues(t, 2, identity.UserAuthVersion)
	response = securityEnrollmentRequest("POST", "/api/user/passkey/register/finish", string(registrationBody), "", identity, PasskeyRegisterFinish)
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	assert.False(t, body.Success)

	response = securityEnrollmentRequest("POST", "/api/user/passkey/verify/begin", `{"scope":"channel.key.read","context":{"channel_id":123}}`, "", identity, PasskeyVerifyBegin)
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	require.NoError(t, common.Unmarshal(body.Data, &begin))
	assertionBody, err := common.Marshal(map[string]any{
		"flow_token": begin.FlowToken, "credential": securityPasskeyResponse(t, key, begin.Options.PublicKey.Challenge, false, 1),
		"scope": "passkey.delete", "context": map[string]any{"channel_id": 456},
	})
	require.NoError(t, err)
	response = securityEnrollmentRequest("POST", "/api/user/passkey/verify/finish", string(assertionBody), "", identity, PasskeyVerifyFinish)
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	var proof service.SecurityProof
	require.NoError(t, common.Unmarshal(body.Data, &proof))
	assert.Equal(t, "passkey", proof.Method)
	assert.Equal(t, "channel.key.read", proof.Scope, "finish cannot replace the operation approved at begin")

	router := gin.New()
	router.POST("/api/channel/:id/key", middleware.RootAuth(), middleware.SecureVerificationRequired(), GetChannelKey)
	for _, test := range []struct {
		name, path, proof, code, key string
		status                       int
	}{
		{"login token only", "/api/channel/123/key", "", "SECURITY_PROOF_REQUIRED", "", http.StatusForbidden},
		{"access token as proof", "/api/channel/123/key", rotation.AccessToken, "SECURITY_PROOF_INVALID", "", http.StatusForbidden},
		{"other channel", "/api/channel/456/key", proof.ProofToken, "SECURITY_PROOF_CONTEXT_MISMATCH", "", http.StatusForbidden},
		{"authorized channel", "/api/channel/123/key", proof.ProofToken, "", "first-channel-secret", http.StatusOK},
		{"replay", "/api/channel/123/key", proof.ProofToken, "SECURITY_PROOF_CONSUMED", "", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", test.path, nil)
			request.Header.Set("Authorization", "Bearer "+rotation.AccessToken)
			request.Header.Set("X-Security-Proof", test.proof)
			result := httptest.NewRecorder()
			router.ServeHTTP(result, request)
			assert.Equal(t, test.status, result.Code)
			var response securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(result.Body.Bytes(), &response))
			assert.Equal(t, test.code, response.Code)
			if test.key != "" {
				assert.Contains(t, string(response.Data), test.key)
			} else {
				assert.NotContains(t, result.Body.String(), "channel-secret")
			}
		})
	}
	var logs []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "channel.key_view").Find(&logs).Error)
	require.Len(t, logs, 1)
	encodedLogs, err := common.Marshal(logs)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedLogs), "first-channel-secret")
	assert.NotContains(t, string(encodedLogs), proof.ProofToken)

	response = securityEnrollmentRequest("POST", "/api/user/passkey/verify/begin", `{"scope":"passkey.delete"}`, "", identity, PasskeyVerifyBegin)
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	require.NoError(t, common.Unmarshal(body.Data, &begin))
	deleteAssertion, err := common.Marshal(passkeyFinishRequest{
		FlowToken: begin.FlowToken, Credential: securityPasskeyResponse(t, key, begin.Options.PublicKey.Challenge, false, 2),
	})
	require.NoError(t, err)
	response = securityEnrollmentRequest("POST", "/api/user/passkey/verify/finish", string(deleteAssertion), "", identity, PasskeyVerifyFinish)
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	require.NoError(t, common.Unmarshal(body.Data, &proof))
	response = securityEnrollmentRequest("DELETE", "/api/user/passkey", "", proof.ProofToken, identity, PasskeyDelete)
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	_, err = model.GetPasskeyByUserID(user.Id)
	assert.ErrorIs(t, err, model.ErrPasskeyNotFound)
}

func TestSecurityEnrollmentVerifyRequiresDedicatedFlowForInteractiveMethods(t *testing.T) {
	for _, method := range []string{service.VerificationMethodPasskey, service.VerificationMethodOAuth} {
		t.Run(method, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			if method == service.VerificationMethodPasskey {
				require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id, CredentialID: "enrolled-passkey", PublicKey: "public-key"}).Error)
			} else {
				require.NoError(t, model.DB.Model(user).Updates(map[string]any{"password": "", "github_id": "linked-user"}).Error)
				oauth.Register("enrollment-oauth", &enrollmentOAuthProvider{externalID: "linked-user"})
				t.Cleanup(func() { oauth.Unregister("enrollment-oauth") })
			}
			_, err := service.RequireVerificationMethod(identity, service.VerificationScopeTwoFASetup, method)
			require.NoError(t, err)
			payload, err := common.Marshal(service.VerificationInput{Method: method, Scope: service.VerificationScopeTwoFASetup})
			require.NoError(t, err)
			response := securityEnrollmentRequest("POST", "/api/verify", string(payload), "", identity, UniversalVerify)
			var body securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.False(t, body.Success)
			assert.Equal(t, "SECURITY_VERIFICATION_FLOW_REQUIRED", body.Code)
			assert.Equal(t, "This verification method requires its dedicated verification flow.", body.Message)
			assert.Empty(t, body.Data)
		})
	}
}

func TestSecurityEnrollmentTwoFAFlowAndSessionRotation(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	other, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "other-device")
	require.NoError(t, err)
	var proof string
	var setups []service.TwoFASetup
	for range 2 {
		proof = issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: "2fa.setup"}, "password")
		response := securityEnrollmentRequest("POST", "/api/user/2fa/setup", `{}`, proof, identity, Setup2FA)
		var body securityEnrollmentResponse
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
		require.True(t, body.Success, body.Message)
		var setup service.TwoFASetup
		require.NoError(t, common.Unmarshal(body.Data, &setup))
		require.NotEmpty(t, setup.FlowToken)
		require.Len(t, setup.BackupCodes, common.BackupCodeCount)
		setups = append(setups, setup)
	}
	oldCode, err := totp.GenerateCode(setups[0].Secret, time.Now())
	require.NoError(t, err)
	assert.ErrorIs(t, service.FinishTwoFASetup(identity, setups[0].FlowToken, oldCode), model.ErrTwoFASetupInvalid)
	assert.ErrorIs(t, service.FinishTwoFASetup(identity, setups[1].FlowToken, "not-a-code"), model.ErrTwoFACodeInvalid)
	_, err = model.GetAuthFlow(setups[1].FlowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeTwoFASetup})
	require.NoError(t, err, "invalid code must not consume setup")
	code, err := totp.GenerateCode(setups[1].Secret, time.Now())
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposeSecurityProof).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	payload, err := common.Marshal(Verify2FARequest{Code: code, FlowToken: setups[1].FlowToken})
	require.NoError(t, err)
	response := securityEnrollmentRequest("POST", "/api/user/2fa/enable", string(payload), "", identity, Enable2FA)
	var body securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	var rotated struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, common.Unmarshal(body.Data, &rotated))
	newIdentity, err := service.ParseAccessToken(rotated.AccessToken)
	require.NoError(t, err)
	assert.EqualValues(t, 2, newIdentity.UserAuthVersion)
	assert.Equal(t, identity.SessionID, newIdentity.SessionID)
	otherSession, err := model.GetUserSessionBySID(other.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusRevoked, otherSession.Status)
	_, err = model.GetAuthFlow(setups[1].FlowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeTwoFASetup})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	_, err = service.ConsumeOperationProof(proof, newIdentity, service.VerificationOperation{Scope: "2fa.setup"})
	assert.Error(t, err)
	enabled, err := model.GetTwoFAByUserId(user.Id)
	require.NoError(t, err)
	assert.True(t, enabled.IsEnabled)
	var logs []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&logs).Error)
	require.NotEmpty(t, logs)
	encoded, err := common.Marshal(logs)
	require.NoError(t, err)
	for _, secret := range []string{setups[0].Secret, setups[1].Secret, proof, code, setups[1].FlowToken} {
		assert.NotContains(t, string(encoded), secret)
	}
}

func TestSecurityEnrollmentSetupAndEnableRollback(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	setup, err := service.StartTwoFASetup(identity, authorizeSecurityEnrollment(t, identity))
	require.NoError(t, err)
	failure := errors.New("injected storage failure")
	authorization := authorizeSecurityEnrollment(t, identity)
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register("security_setup_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "auth_flows" {
			tx.AddError(failure)
		}
	}))
	_, err = service.StartTwoFASetup(identity, authorization)
	assert.ErrorIs(t, err, failure)
	require.NoError(t, model.DB.Callback().Create().Remove("security_setup_failure"))
	pending, err := model.GetTwoFAByUserId(user.Id)
	require.NoError(t, err)
	assert.Equal(t, setup.Secret, pending.Secret)
	count, err := model.GetUnusedBackupCodeCount(user.Id)
	require.NoError(t, err)
	assert.Equal(t, common.BackupCodeCount, count)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("security_enable_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "two_fas" {
			tx.AddError(failure)
		}
	}))
	assert.ErrorIs(t, service.FinishTwoFASetup(identity, setup.FlowToken, code), failure)
	require.NoError(t, model.DB.Callback().Update().Remove("security_enable_failure"))
	_, err = model.GetAuthFlow(setup.FlowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeTwoFASetup})
	require.NoError(t, err)
	storedUser, err := model.GetUserById(user.Id, false)
	require.NoError(t, err)
	assert.Equal(t, identity.UserAuthVersion, storedUser.AuthVersion)
	require.NoError(t, service.FinishTwoFASetup(identity, setup.FlowToken, code))
}

type enrollmentOAuthProvider struct {
	authFlowTestOAuthProvider
	externalID string
	disabled   bool
}

func (*enrollmentOAuthProvider) ProviderUserIDColumn() string { return "github_id" }
func (p *enrollmentOAuthProvider) IsEnabled() bool            { return !p.disabled }
func (p *enrollmentOAuthProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	return &oauth.OAuthUser{ProviderUserID: p.externalID, Email: "same@example.com"}, nil
}

func TestSecurityEnrollmentOAuthVerificationNeverChangesLoginOrBindings(t *testing.T) {
	for _, scenario := range []string{"success", "different account", "binding changed", "session changed", "auth version changed", "provider disabled", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			require.NoError(t, model.DB.Model(user).Updates(map[string]any{"password": "", "github_id": "linked-user", "email": "same@example.com"}).Error)
			provider := &enrollmentOAuthProvider{externalID: "linked-user"}
			oauth.Register("enrollment-oauth", provider)
			t.Cleanup(func() { oauth.Unregister("enrollment-oauth") })
			response := securityEnrollmentRequest("POST", "/api/oauth/state", `{"provider":"enrollment-oauth","intent":"verify","scope":"2fa.setup"}`, "", identity, GenerateOAuthCode)
			var body securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			require.True(t, body.Success, body.Message)
			var started struct {
				FlowToken string `json:"flow_token"`
			}
			require.NoError(t, common.Unmarshal(body.Data, &started))
			callbackIdentity := identity
			query := "&code=authorization-code"
			switch scenario {
			case "different account":
				provider.externalID = "other-user"
			case "binding changed":
				require.NoError(t, model.DB.Model(user).Update("github_id", "replacement-user").Error)
			case "session changed":
				callbackIdentity.SessionID = "different-session"
			case "auth version changed":
				require.NoError(t, model.DB.Model(user).Update("auth_version", 2).Error)
			case "cancelled":
				query = "&error=access_denied"
			case "provider disabled":
				provider.disabled = true
			}
			handler := func(c *gin.Context) {
				c.Params = gin.Params{{Key: "provider", Value: "enrollment-oauth"}}
				HandleOAuth(c)
			}
			path := "/api/oauth/enrollment-oauth?state=" + started.FlowToken + query
			response = securityEnrollmentRequest("GET", path, "", "", callbackIdentity, handler)
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, scenario == "success", body.Success, body.Message)
			assert.NotContains(t, response.Body.String(), "access_token")
			assert.Empty(t, response.Header().Values("Set-Cookie"))
			if scenario == "success" {
				var proof service.SecurityProof
				require.NoError(t, common.Unmarshal(body.Data, &proof))
				_, err := service.ConsumeOperationProof(proof.ProofToken, identity, service.VerificationOperation{Scope: "2fa.setup"})
				require.NoError(t, err)
				replayed := securityEnrollmentRequest("GET", path, "", "", identity, handler)
				assert.Equal(t, http.StatusForbidden, replayed.Code)
			}
			var users, sessions, bindings int64
			require.NoError(t, model.DB.Model(&model.User{}).Count(&users).Error)
			require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&sessions).Error)
			require.NoError(t, model.DB.Model(&model.UserOAuthBinding{}).Count(&bindings).Error)
			assert.EqualValues(t, 1, users)
			assert.EqualValues(t, 1, sessions)
			assert.Zero(t, bindings)
			stored, err := model.GetUserById(user.Id, false)
			require.NoError(t, err)
			expectedBinding := "linked-user"
			if scenario == "binding changed" {
				expectedBinding = "replacement-user"
			}
			assert.Equal(t, expectedBinding, stored.GitHubId)
		})
	}
}

func TestSecurityEnrollmentEncryptedPasswordVerification(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	keyID, publicPEM := common.PasswordEncryptionPublicKey()
	if keyID == "" {
		privatePEM, err := common.GeneratePasswordEncryptionPrivateKey()
		require.NoError(t, err)
		require.NoError(t, common.LoadPasswordEncryptionPrivateKey(privatePEM))
		keyID, publicPEM = common.PasswordEncryptionPublicKey()
	}
	block, _ := pem.Decode([]byte(publicPEM))
	require.NotNil(t, block)
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	publicKey, ok := parsed.(*rsa.PublicKey)
	require.True(t, ok)
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, []byte("enrollment-password"), nil)
	require.NoError(t, err)
	common.PasswordLoginEncryptionEnabled = true
	input := service.VerificationInput{Method: "password", Scope: "passkey.register", PasswordEncrypted: base64.StdEncoding.EncodeToString(ciphertext), EncryptionKeyID: keyID}
	proof, err := service.VerifySecurityInput(identity, input)
	require.NoError(t, err)
	_, err = service.ConsumeOperationProof(proof.ProofToken, identity, service.VerificationOperation{Scope: input.Scope})
	require.NoError(t, err)
	input.EncryptionKeyID = "incorrect-key-id"
	_, err = service.VerifySecurityInput(identity, input)
	assert.ErrorIs(t, err, service.ErrVerificationFailed)
}

func TestSecurityEnrollmentTwoFAFailureAccountingAndStorageErrors(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	twoFA := &model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}
	require.NoError(t, model.DB.Create(twoFA).Error)
	for _, endpoint := range []struct {
		path    string
		handler gin.HandlerFunc
	}{
		{"/api/user/2fa/disable", Disable2FA},
		{"/api/user/2fa/backup_codes", RegenerateBackupCodes},
		{"/api/user/login/2fa", Verify2FALogin},
	} {
		response := securityEnrollmentRequest("POST", endpoint.path, `{}`, "", identity, endpoint.handler)
		var body securityEnrollmentResponse
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
		assert.False(t, body.Success, endpoint.path)
		if endpoint.path == "/api/user/login/2fa" {
			assert.Equal(t, "参数错误", body.Message, endpoint.path)
		} else {
			assert.Equal(t, "SECURITY_PROOF_REQUIRED", body.Code, endpoint.path)
		}
	}
	stored, err := model.GetTwoFAByUserId(user.Id)
	require.NoError(t, err)
	assert.Zero(t, stored.FailedAttempts, "missing required input must not count as a failed verification")
	wrongCode := ""
	for _, candidate := range []string{"000000", "111111", "222222", "333333"} {
		if !common.ValidateTOTPCode(twoFA.Secret, candidate) {
			wrongCode = candidate
			break
		}
	}
	require.NotEmpty(t, wrongCode)
	assert.ErrorIs(t, service.VerifyTwoFactorCode(twoFA, wrongCode), service.ErrVerificationFailed)
	stored, err = model.GetTwoFAByUserId(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 1, stored.FailedAttempts)
	hash, err := common.HashBackupCode("ABCD-1234")
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.TwoFABackupCode{UserId: user.Id, CodeHash: hash}).Error)
	require.NoError(t, service.VerifyTwoFactorCode(stored, "ABCD-1234"))
	assert.ErrorIs(t, service.VerifyTwoFactorCode(stored, "ABCD-1234"), service.ErrVerificationFailed)
	until := time.Now().Add(time.Minute)
	stored.LockedUntil = &until
	assert.ErrorIs(t, service.VerifyTwoFactorCode(stored, "ABCD-1234"), service.ErrVerificationLocked)
	stored.LockedUntil = nil
	code, err := totp.GenerateCode(stored.Secret, time.Now())
	require.NoError(t, err)
	failure := errors.New("usage storage failed")
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("security_usage_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "two_fas" {
			tx.AddError(failure)
		}
	}))
	assert.ErrorIs(t, service.VerifyTwoFactorCode(stored, code), failure)
	require.NoError(t, model.DB.Callback().Update().Remove("security_usage_failure"))
}

func TestSecurityEnrollmentDatabaseErrorsAreNotReturned(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body, table string
		create                          bool
		handler                         gin.HandlerFunc
	}{
		{"methods", "GET", "/api/verify/methods?scope=2fa.setup", "", "users", false, GetVerificationMethods},
		{"password", "POST", "/api/verify", `{"method":"password","scope":"2fa.setup","password":"enrollment-password"}`, "users", false, UniversalVerify},
		{"passkey", "POST", "/api/user/passkey/register/begin", "", "users", false, PasskeyRegisterBegin},
		{"2fa status", "GET", "/api/user/2fa/status", "", "two_fas", false, Get2FAStatus},
		{"2fa setup", "POST", "/api/user/2fa/setup", "", "two_fas", true, Setup2FA},
		{"2fa flow", "POST", "/api/user/2fa/setup", "", "auth_flows", true, Setup2FA},
		{"oauth state", "POST", "/api/oauth/state", `{"provider":"enrollment-oauth","intent":"verify","scope":"2fa.setup"}`, "users", false, GenerateOAuthCode},
		{"channel key", "POST", "/api/channel/1/key", "", "channels", false, func(c *gin.Context) {
			c.Params = gin.Params{{Key: "id", Value: "1"}}
			GetChannelKey(c)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, identity := setupSecurityEnrollmentTest(t)
			oauth.Register("enrollment-oauth", &enrollmentOAuthProvider{externalID: "linked-user"})
			t.Cleanup(func() { oauth.Unregister("enrollment-oauth") })
			proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeTwoFASetup}, service.VerificationMethodPassword)
			privateError := errors.New("database connection failed: private-db-host private_table SELECT secret_column")
			callback := func(tx *gorm.DB) {
				if tx.Statement.Table == test.table {
					tx.AddError(privateError)
				}
			}
			if test.create {
				require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register("security_private_error", callback))
			} else {
				require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register("security_private_error", callback))
			}
			response := securityEnrollmentRequest(test.method, test.path, test.body, proof, identity, test.handler)
			var body securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, http.StatusInternalServerError, response.Code)
			assert.False(t, body.Success)
			assert.Equal(t, "AUTH_INTERNAL_ERROR", body.Code)
			assert.Equal(t, "Internal Server Error", body.Message)
			assert.NotContains(t, response.Body.String(), "private")
			assert.NotContains(t, response.Body.String(), "SELECT")
		})
	}
}

func TestSecurityEnrollmentPublicErrorsDiscardWrappedDetails(t *testing.T) {
	for _, test := range []struct {
		err     error
		code    string
		message string
	}{
		{service.ErrVerificationFailed, "SECURITY_VERIFICATION_FAILED", service.ErrVerificationFailed.Error()},
		{service.ErrVerificationLocked, "SECURITY_VERIFICATION_LOCKED", service.ErrVerificationLocked.Error()},
		{service.ErrOAuthAccountMismatch, "OAUTH_ACCOUNT_MISMATCH", service.ErrOAuthAccountMismatch.Error()},
		{model.ErrTwoFASetupInvalid, "TWOFA_SETUP_INVALID", model.ErrTwoFASetupInvalid.Error()},
	} {
		t.Run(test.code, func(t *testing.T) {
			response := securityEnrollmentRequest("POST", "/api/verify", "", "", service.AuthIdentity{}, func(c *gin.Context) {
				writeSecurityOperationError(c, fmt.Errorf("private database details: %w", test.err))
			})
			var body securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			assert.False(t, body.Success)
			assert.Equal(t, test.code, body.Code)
			assert.Equal(t, test.message, body.Message)
			assert.NotContains(t, response.Body.String(), "private")
		})
	}
}

func TestSecurityEnrollmentExpiredAndCrossSessionSetupsCannotActivate(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	setup, err := service.StartTwoFASetup(identity, authorizeSecurityEnrollment(t, identity))
	require.NoError(t, err)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	other, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "other-session")
	require.NoError(t, err)
	otherIdentity, err := service.ParseAccessToken(other.AccessToken)
	require.NoError(t, err)
	assert.ErrorIs(t, service.FinishTwoFASetup(otherIdentity, setup.FlowToken, code), model.ErrTwoFASetupInvalid)
	flow, err := model.GetAuthFlow(setup.FlowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeTwoFASetup})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(flow).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	assert.ErrorIs(t, service.FinishTwoFASetup(identity, setup.FlowToken, code), model.ErrTwoFASetupInvalid)
	stored, err := model.GetTwoFAByUserId(user.Id)
	require.NoError(t, err)
	assert.False(t, stored.IsEnabled)
	storedUser, err := model.GetUserById(user.Id, false)
	require.NoError(t, err)
	assert.Equal(t, identity.UserAuthVersion, storedUser.AuthVersion)
}

func TestSecurityEnrollmentOAuthQueriesReachHandlerWithoutLeakingToAccessLogs(t *testing.T) {
	var output bytes.Buffer
	previous := gin.DefaultWriter
	gin.DefaultWriter = &output
	t.Cleanup(func() { gin.DefaultWriter = previous })
	router := gin.New()
	middleware.SetUpLogger(router)
	router.GET("/api/oauth/:provider", func(c *gin.Context) {
		assert.Equal(t, "private-code", c.Query("code"))
		assert.Equal(t, "private-state", c.Query("state"))
		c.Status(http.StatusNoContent)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/api/oauth/github?code=private-code&state=private-state", nil))
	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.Contains(t, output.String(), "/api/oauth/github")
	assert.NotContains(t, output.String(), "private-code")
	assert.NotContains(t, output.String(), "private-state")
}

func TestSecurityEnrollmentCustomOAuthUsesExistingBinding(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.Model(user).Update("password", "").Error)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			assert.NoError(t, r.ParseForm())
			assert.Equal(t, "custom-code", r.Form.Get("code"))
			_, _ = w.Write([]byte(`{"access_token":"provider-access-token","token_type":"Bearer"}`))
		case "/userinfo":
			assert.Equal(t, "Bearer provider-access-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"sub":"custom-user","name":"Existing user"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	provider := oauth.NewGenericOAuthProvider(&model.CustomOAuthProvider{
		Id: 42, Slug: "enrollment-custom", Name: "Custom provider", Enabled: true,
		ClientId: "client", ClientSecret: "secret", UserIdField: "sub",
		TokenEndpoint: upstream.URL + "/token", UserInfoEndpoint: upstream.URL + "/userinfo",
	})
	oauth.RegisterCustom("enrollment-custom", provider)
	t.Cleanup(func() { oauth.Unregister("enrollment-custom") })
	require.NoError(t, model.DB.Create(&model.UserOAuthBinding{UserId: user.Id, ProviderId: 42, ProviderUserId: "custom-user"}).Error)
	response := securityEnrollmentRequest("GET", "/api/verify/methods?scope=2fa.setup", "", "", identity, GetVerificationMethods)
	assert.Contains(t, response.Body.String(), "enrollment-custom")
	assert.NotContains(t, response.Body.String(), "custom-user")
	response = securityEnrollmentRequest("POST", "/api/oauth/state", `{"provider":"enrollment-custom","intent":"verify","scope":"2fa.setup"}`, "", identity, GenerateOAuthCode)
	var body securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	var started struct {
		FlowToken string `json:"flow_token"`
	}
	require.NoError(t, common.Unmarshal(body.Data, &started))
	response = securityEnrollmentRequest("GET", "/api/oauth/enrollment-custom?state="+started.FlowToken+"&code=custom-code", "", "", identity, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "provider", Value: "enrollment-custom"}}
		HandleOAuth(c)
	})
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, body.Message)
	var proof service.SecurityProof
	require.NoError(t, common.Unmarshal(body.Data, &proof))
	_, err := service.ConsumeOperationProof(proof.ProofToken, identity, service.VerificationOperation{Scope: "2fa.setup"})
	require.NoError(t, err)
	bindings, err := model.GetUserOAuthBindingsByUserId(user.Id)
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	assert.Equal(t, "custom-user", bindings[0].ProviderUserId)
}

func completeFirstSecurityFactor(t *testing.T, identity service.AuthIdentity, proof service.SecurityProof) {
	t.Helper()
	var response *httptest.ResponseRecorder
	if proof.Scope == service.VerificationScopeTwoFASetup {
		response = securityEnrollmentRequest("POST", "/api/user/2fa/setup", "", proof.ProofToken, identity, Setup2FA)
		var body securityEnrollmentResponse
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
		require.True(t, body.Success, body.Message)
		var setup service.TwoFASetup
		require.NoError(t, common.Unmarshal(body.Data, &setup))
		code, err := totp.GenerateCode(setup.Secret, time.Now())
		require.NoError(t, err)
		request, err := common.Marshal(Verify2FARequest{FlowToken: setup.FlowToken, Code: code})
		require.NoError(t, err)
		response = securityEnrollmentRequest("POST", "/api/user/2fa/enable", string(request), "", identity, Enable2FA)
	} else {
		response = securityEnrollmentRequest("POST", "/api/user/passkey/register/begin", "", proof.ProofToken, identity, PasskeyRegisterBegin)
		var body securityEnrollmentResponse
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
		require.True(t, body.Success, body.Message)
		var begin struct {
			FlowToken string `json:"flow_token"`
			Options   struct {
				PublicKey struct {
					Challenge string `json:"challenge"`
				} `json:"publicKey"`
			} `json:"options"`
		}
		require.NoError(t, common.Unmarshal(body.Data, &begin))
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		request, err := common.Marshal(passkeyFinishRequest{
			FlowToken:  begin.FlowToken,
			Credential: securityPasskeyResponse(t, key, begin.Options.PublicKey.Challenge, true, 0),
		})
		require.NoError(t, err)
		response = securityEnrollmentRequest("POST", "/api/user/passkey/register/finish", string(request), "", identity, PasskeyRegisterFinish)
	}
	var body securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, response.Body.String())
	var rotation struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, common.Unmarshal(body.Data, &rotation))
	updated, err := service.ParseAccessToken(rotation.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, identity.UserID, updated.UserID)
	assert.Equal(t, identity.UserAuthVersion+1, updated.UserAuthVersion)
	_, err = service.ConsumeOperationProof(proof.ProofToken, updated, service.VerificationOperation{Scope: proof.Scope})
	assert.Error(t, err)
}

func TestSecurityEnrollmentTelegramAndWeChatFirstFactor(t *testing.T) {
	for _, provider := range []string{"telegram", "wechat"} {
		for _, scope := range []string{service.VerificationScopeTwoFASetup, service.VerificationScopePasskeyRegister} {
			t.Run(provider+"/"+scope, func(t *testing.T) {
				var user *model.User
				var identity service.AuthIdentity
				var response *httptest.ResponseRecorder
				method := service.VerificationMethodSession
				if provider == "telegram" {
					fixture := setupTelegramOAuthTest(t)
					user, identity = fixture.user, fixture.identity
					require.NoError(t, model.DB.Model(user).Updates(map[string]any{"password": "", "telegram_id": "42"}).Error)
					state, code := fixture.authorization(t, "verify", identity, scope, telegramIdentityClaims(99))
					mismatch := telegramOAuthCallback(state, code, identity)
					assert.Contains(t, mismatch.Body.String(), "OAUTH_ACCOUNT_MISMATCH")
					assert.NotContains(t, mismatch.Body.String(), "proof_token")
					state, code = fixture.authorization(t, "verify", identity, scope, telegramIdentityClaims(42))
					response = telegramOAuthCallback(state, code, identity)
					method = service.VerificationMethodOAuth
				} else {
					user, identity = setupSecurityEnrollmentTest(t)
					require.NoError(t, model.DB.Model(user).Updates(map[string]any{"password": "", "wechat_id": "wechat-user"}).Error)
					request, err := common.Marshal(service.VerificationInput{Scope: scope, Method: method})
					require.NoError(t, err)
					response = securityEnrollmentRequest("POST", "/api/verify", string(request), "", identity, UniversalVerify)
				}
				var body securityEnrollmentResponse
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
				if provider == "wechat" {
					assert.False(t, body.Success)
					assert.NotContains(t, response.Body.String(), "proof_token")
					return
				}
				require.True(t, body.Success, response.Body.String())
				var proof service.SecurityProof
				require.NoError(t, common.Unmarshal(body.Data, &proof))
				assert.Equal(t, scope, proof.Scope)
				assert.Equal(t, method, proof.Method)
				before, err := model.GetUserById(user.Id, true)
				require.NoError(t, err)
				assert.Empty(t, before.Password)
				assert.Equal(t, identity.UserAuthVersion, before.AuthVersion)
				completeFirstSecurityFactor(t, identity, proof)
				after, err := model.GetUserById(user.Id, true)
				require.NoError(t, err)
				assert.Equal(t, before.TelegramId, after.TelegramId)
				assert.Equal(t, before.WeChatId, after.WeChatId)
			})
		}
	}
}

func TestSecurityEnrollmentNeverTrustsSessionForFirstFactor(t *testing.T) {
	for _, scenario := range []string{"wechat only", "password", "passkey", "locked 2fa", "telegram", "github", "disabled custom binding", "no binding", "binding storage failure", "revoked session"} {
		t.Run(scenario, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			require.NoError(t, model.DB.Model(user).Updates(map[string]any{"password": "", "wechat_id": "wechat-user"}).Error)
			switch scenario {
			case "password":
				require.NoError(t, model.DB.Model(user).Update("password", "stored-hash").Error)
			case "passkey":
				require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id, CredentialID: "credential", PublicKey: "key"}).Error)
			case "locked 2fa":
				until := time.Now().Add(time.Hour)
				require.NoError(t, model.DB.Create(&model.TwoFA{UserId: user.Id, Secret: "secret", IsEnabled: true, LockedUntil: &until}).Error)
			case "telegram":
				require.NoError(t, model.DB.Model(user).Update("telegram_id", "42").Error)
			case "github":
				require.NoError(t, model.DB.Model(user).Update("github_id", "42").Error)
			case "disabled custom binding":
				require.NoError(t, model.DB.Create(&model.UserOAuthBinding{UserId: user.Id, ProviderId: 42, ProviderUserId: "linked"}).Error)
			case "no binding":
				require.NoError(t, model.DB.Model(user).Update("wechat_id", "").Error)
			case "binding storage failure":
				require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register("wechat_bindings_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "user_oauth_bindings" {
						tx.AddError(errors.New("private binding failure"))
					}
				}))
			case "revoked session":
				_, err := model.RevokeAllUserSessions(user.Id, "test")
				require.NoError(t, err)
			}
			for _, scope := range []string{"2fa.setup", "passkey.register", "passkey.delete", "channel.key.read"} {
				context := json.RawMessage(nil)
				if scope == "channel.key.read" {
					context = json.RawMessage(`{"channel_id":1}`)
				}
				_, err := service.VerifySecurityInput(identity, service.VerificationInput{Method: "session", Scope: scope, Context: context})
				assert.Error(t, err, scope)
			}
			if scenario == "binding storage failure" {
				require.NoError(t, model.DB.Callback().Query().Remove("wechat_bindings_failure"))
			}
			var count int64
			require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposeSecurityProof).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestSecurityEnrollmentMissingTargetsAreBusinessErrors(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}))
	for _, target := range []struct {
		path, key string
		handler   gin.HandlerFunc
	}{
		{"/api/channel/999/key", i18n.MsgChannelNotExists, GetChannelKey},
		{"/api/user/999/2fa", i18n.MsgUserNotExists, AdminDisable2FA},
	} {
		var expectedMessage string
		response := securityEnrollmentRequest("POST", target.path, "", "", identity, func(c *gin.Context) {
			c.Params = gin.Params{{Key: "id", Value: "999"}}
			expectedMessage = i18n.T(c, target.key)
			target.handler(c)
		})
		assert.Equal(t, http.StatusOK, response.Code)
		var body securityEnrollmentResponse
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
		assert.False(t, body.Success)
		assert.Equal(t, expectedMessage, body.Message)
		assert.NotEqual(t, "AUTH_UNAUTHORIZED", body.Code)
		for _, id := range []string{"invalid", "0", "-1"} {
			invalid := securityEnrollmentRequest("POST", target.path, "", "", identity, func(c *gin.Context) {
				c.Params = gin.Params{{Key: "id", Value: id}}
				target.handler(c)
			})
			assert.Equal(t, http.StatusOK, invalid.Code)
			assert.Contains(t, invalid.Body.String(), `"success":false`)
		}
		require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register("target_query_failure", func(tx *gorm.DB) {
			if tx.Statement.Table == "users" || tx.Statement.Table == "channels" {
				tx.AddError(errors.New("private database failure"))
			}
		}))
		failed := securityEnrollmentRequest("POST", target.path, "", "", identity, func(c *gin.Context) {
			c.Params = gin.Params{{Key: "id", Value: "999"}}
			target.handler(c)
		})
		require.NoError(t, model.DB.Callback().Query().Remove("target_query_failure"))
		assert.Equal(t, http.StatusInternalServerError, failed.Code)
		assert.NotContains(t, failed.Body.String(), "private database failure")
	}
	_, _, err := service.ValidateLoginSession(identity)
	require.NoError(t, err)
}

func TestSecurityEnrollmentRejectsChangedFirstFactorPolicy(t *testing.T) {
	for _, provider := range []string{"telegram", "wechat"} {
		for _, stage := range []string{"proof", "setup"} {
			t.Run(provider+"/"+stage, func(t *testing.T) {
				fixture := setupTelegramOAuthTest(t)
				user, identity := fixture.user, fixture.identity
				require.NoError(t, model.DB.Model(user).Update("password", "").Error)
				var proof *service.SecurityProof
				var err error
				if provider == "telegram" {
					require.NoError(t, model.DB.Model(user).Update("telegram_id", "42").Error)
					state, code := fixture.authorization(t, "verify", identity, "2fa.setup", telegramIdentityClaims(42))
					response := telegramOAuthCallback(state, code, identity)
					var body securityEnrollmentResponse
					require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
					require.True(t, body.Success, response.Body.String())
					require.NoError(t, common.Unmarshal(body.Data, &proof))
				} else {
					require.NoError(t, model.DB.Model(user).Update("wechat_id", "wechat-user").Error)
					proof, err = service.VerifySecurityInput(identity, service.VerificationInput{Scope: "2fa.setup", Method: "session"})
					require.Error(t, err)
					assert.Nil(t, proof)
					return
				}
				_, err = service.ConsumeOperationProof(proof.ProofToken, identity, service.VerificationOperation{Scope: "passkey.register"})
				assert.Error(t, err)
				var setupToken string
				if stage == "setup" {
					response := securityEnrollmentRequest("POST", "/api/user/2fa/setup", `{}`, proof.ProofToken, identity, Setup2FA)
					var body securityEnrollmentResponse
					require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
					require.True(t, body.Success, response.Body.String())
					var setup struct {
						FlowToken string `json:"flow_token"`
					}
					require.NoError(t, common.Unmarshal(body.Data, &setup))
					setupToken = setup.FlowToken
					_, err = service.ConsumeOperationProof(proof.ProofToken, identity, service.VerificationOperation{Scope: "2fa.setup"})
					assert.ErrorIs(t, err, service.ErrProofConsumed)
				}
				if provider == "telegram" {
					common.TelegramOAuthEnabled = false
				} else {
					require.NoError(t, model.DB.Model(user).Update("telegram_id", "42").Error)
				}
				if stage == "proof" {
					response := securityEnrollmentRequest("POST", "/api/user/2fa/setup", `{}`, proof.ProofToken, identity, Setup2FA)
					assert.Contains(t, response.Body.String(), `"success":false`)
					assert.NotContains(t, response.Body.String(), "flow_token")
				} else {
					request, err := common.Marshal(map[string]string{"flow_token": setupToken, "code": "123456"})
					require.NoError(t, err)
					response := securityEnrollmentRequest("POST", "/api/user/2fa/enable", string(request), "", identity, Enable2FA)
					assert.Contains(t, response.Body.String(), `"success":false`)
				}
				factor, err := model.GetTwoFAByUserId(user.Id)
				require.NoError(t, err)
				assert.True(t, factor == nil || !factor.IsEnabled)
			})
		}
	}
}
