package controller

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSecurityAccountDeletionRequiresScopedProof(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong scope", "expired", "consumed", "other session", "other account", "password disabled", "factor added"} {
		t.Run(scenario, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			originalIdentity := identity
			operation := service.VerificationOperation{Scope: service.VerificationScopeAccountDelete}
			proof := ""
			if scenario != "missing" {
				proof = issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
			}
			if scenario == "wrong scope" {
				proof = issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopePasswordChange}, service.VerificationMethodPassword)
			}
			switch scenario {
			case "expired":
				require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposeSecurityProof).Update("expires_at", time.Now().Add(-time.Minute)).Error)
			case "consumed":
				_, err := service.ConsumeOperationProof(proof, identity, operation)
				require.NoError(t, err)
			case "other session", "other account":
				userID := user.Id
				if scenario == "other account" {
					other := &model.User{Username: "other", AffCode: "other", Group: "default", Password: user.Password, Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1}
					require.NoError(t, model.DB.Create(other).Error)
					userID = other.Id
				}
				bundle, err := service.CreateLoginSession(userID, "password", "127.0.0.1", scenario)
				require.NoError(t, err)
				identity, err = service.ParseAccessToken(bundle.AccessToken)
				require.NoError(t, err)
			case "password disabled":
				previous := common.PasswordLoginEnabled
				common.PasswordLoginEnabled = false
				t.Cleanup(func() { common.PasswordLoginEnabled = previous })
			case "factor added":
				require.NoError(t, model.DB.Create(&model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}).Error)
			}
			response := securityEnrollmentRequest("DELETE", "/api/user/self", "", proof, identity, DeleteSelf)
			assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
			stored, err := model.GetUserById(user.Id, false)
			require.NoError(t, err)
			assert.Equal(t, identity.UserAuthVersion, stored.AuthVersion)
			_, _, err = service.ValidateLoginSession(originalIdentity)
			assert.NoError(t, err)
			var audit model.AuditLog
			require.NoError(t, model.LOG_DB.Where("action = ?", "user.account_delete").Last(&audit).Error)
			assert.False(t, audit.Success)
			auditJSON, err := common.Marshal(audit)
			require.NoError(t, err)
			if proof != "" {
				assert.NotContains(t, string(auditJSON), proof)
			}
		})
	}
}

func TestSecurityAccountDeletionAcceptsEitherFactorAndRevokesSessions(t *testing.T) {
	for _, method := range []string{"password", "oauth", "2fa", "passkey"} {
		t.Run(method, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			if method == "oauth" {
				require.NoError(t, model.DB.Model(user).Updates(map[string]any{"password": "", "github_id": "linked-user"}).Error)
				oauth.Register("account-delete-oauth", &enrollmentOAuthProvider{externalID: "linked-user"})
				t.Cleanup(func() { oauth.Unregister("account-delete-oauth") })
			}
			if method == "2fa" || method == "passkey" {
				newSecurityLoginPasskey(t, user.Id)
				factor := &model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}
				if method == "passkey" {
					locked := time.Now().Add(time.Minute)
					factor.LockedUntil = &locked
				} else {
					system_setting.GetPasskeySettings().Enabled = false
				}
				require.NoError(t, model.DB.Create(factor).Error)
			}
			requirements, err := service.GetVerificationRequirements(identity, service.VerificationScopeAccountDelete)
			require.NoError(t, err)
			_, err = service.RequireVerificationMethod(identity, service.VerificationScopeAccountDelete, method)
			require.NoError(t, err)
			if method == "2fa" || method == "passkey" {
				assert.Len(t, requirements.Methods, 2)
				_, err = service.RequireVerificationMethod(identity, service.VerificationScopeAccountDelete, "password")
				assert.ErrorIs(t, err, service.ErrProofMethod)
			}
			proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeAccountDelete}, method)
			if method == "password" || method == "2fa" {
				input := service.VerificationInput{Scope: service.VerificationScopeAccountDelete, Method: method, Password: "enrollment-password"}
				if method == "2fa" {
					input.Code, err = totp.GenerateCode("JBSWY3DPEHPK3PXP", time.Now())
					require.NoError(t, err)
				}
				verified, err := service.VerifySecurityInput(identity, input)
				require.NoError(t, err)
				proof = verified.ProofToken
			}
			otherSession, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "second-session")
			require.NoError(t, err)
			require.NoError(t, model.UpdateUserAccessToken(user.Id, "account-delete-access-token"))
			response := securityEnrollmentRequest("DELETE", "/api/user/self", "", proof, identity, DeleteSelf)
			var result securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			require.True(t, result.Success, response.Body.String())
			assert.Contains(t, response.Header().Get("Set-Cookie"), "Max-Age=0")
			assert.Contains(t, response.Header().Get("Cache-Control"), "no-store")
			var deleted model.User
			require.NoError(t, model.DB.Unscoped().First(&deleted, user.Id).Error)
			assert.True(t, deleted.DeletedAt.Valid)
			assert.Equal(t, user.AuthVersion+1, deleted.AuthVersion)
			_, _, err = service.ValidateLoginSession(identity)
			assert.Error(t, err)
			otherIdentity, err := service.ParseAccessToken(otherSession.AccessToken)
			require.NoError(t, err)
			_, _, err = service.ValidateLoginSession(otherIdentity)
			assert.Error(t, err)
			_, _, err = service.RefreshLoginSession(otherSession.RefreshToken, otherIdentity.SessionID, "127.0.0.1", "second-session")
			assert.Error(t, err)
			count, err := model.CountActiveUserSessions(user.Id, time.Now().Unix())
			require.NoError(t, err)
			assert.Zero(t, count)
			tokenUser, err := model.ValidateAccessToken("account-delete-access-token")
			require.NoError(t, err)
			assert.Nil(t, tokenUser)
			var audit model.AuditLog
			require.NoError(t, model.LOG_DB.Where("action = ?", "user.account_delete").Last(&audit).Error)
			assert.True(t, audit.Success)
		})
	}
}

func TestSecurityAccountDeletionRechecksTransactionAndConsumesFailedProof(t *testing.T) {
	for _, scenario := range []string{"revoked session", "auth version", "root", "write failure"} {
		t.Run(scenario, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			operation := service.VerificationOperation{Scope: service.VerificationScopeAccountDelete}
			proof := issueSecurityEnrollmentProof(t, identity, operation, "password")
			if scenario != "write failure" {
				_, err := service.ConsumeOperationProof(proof, identity, operation)
				require.NoError(t, err)
			}
			switch scenario {
			case "revoked session":
				_, err := model.RevokeAllUserSessions(user.Id, "test")
				require.NoError(t, err)
			case "auth version":
				require.NoError(t, model.DB.Model(user).Update("auth_version", user.AuthVersion+1).Error)
			case "root":
				require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
				_, err := service.GetVerificationRequirements(identity, service.VerificationScopeAccountDelete)
				assert.ErrorIs(t, err, service.ErrVerificationForbidden)
			case "write failure":
				require.NoError(t, model.DB.Callback().Delete().Before("gorm:delete").Register("account-delete-failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						_ = tx.AddError(errors.New("injected deletion failure"))
					}
				}))
				response := securityEnrollmentRequest("DELETE", "/api/user/self", "", proof, identity, DeleteSelf)
				assert.Contains(t, response.Body.String(), `"success":false`)
				require.NoError(t, model.DB.Callback().Delete().Remove("account-delete-failure"))
				response = securityEnrollmentRequest("DELETE", "/api/user/self", "", proof, identity, DeleteSelf)
				assert.Contains(t, response.Body.String(), "SECURITY_PROOF_CONSUMED")
			}
			if scenario != "write failure" {
				assert.Error(t, model.DeleteUserForSession(identity))
			}
			_, err := model.GetUserById(user.Id, false)
			require.NoError(t, err)
			if scenario == "write failure" {
				_, _, err = service.ValidateLoginSession(identity)
				assert.NoError(t, err, "failed deletion must roll back the account version and preserve sessions")
			}
		})
	}
}

func TestSecurityAccountDeletionConcurrentRequestsHaveOneWinner(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeAccountDelete}, "password")
	start := make(chan struct{})
	responses := make(chan string, 2)
	for range 2 {
		go func() {
			<-start
			response := securityEnrollmentRequest("DELETE", "/api/user/self", "", proof, identity, DeleteSelf)
			responses <- response.Body.String()
		}()
	}
	close(start)
	succeeded := 0
	for range 2 {
		var response securityEnrollmentResponse
		require.NoError(t, common.UnmarshalJsonStr(<-responses, &response))
		if response.Success {
			succeeded++
		}
	}
	assert.Equal(t, 1, succeeded)
	var deleted model.User
	require.NoError(t, model.DB.Unscoped().First(&deleted, user.Id).Error)
	assert.True(t, deleted.DeletedAt.Valid)
	assert.Equal(t, identity.UserAuthVersion+1, deleted.AuthVersion)
}

type securityMailbox struct {
	mutex sync.Mutex
	mail  map[string][]string
}

// Use a real local SMTP boundary so delivery, failure and code handling are
// exercised without exporting test-only mail hooks from production packages.
func newSecurityMailbox(t *testing.T) *securityMailbox {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	mailbox := &securityMailbox{mail: map[string][]string{}}
	previousServer, previousPort := common.SMTPServer, common.SMTPPort
	previousAccount, previousFrom, previousToken := common.SMTPAccount, common.SMTPFrom, common.SMTPToken
	previousSSL, previousTLS := common.SMTPSSLEnabled, common.SMTPStartTLSEnabled
	common.SMTPServer, common.SMTPPort = "127.0.0.1", listener.Addr().(*net.TCPAddr).Port
	common.SMTPAccount, common.SMTPFrom, common.SMTPToken = "", "sender@example.com", ""
	common.SMTPSSLEnabled, common.SMTPStartTLSEnabled = false, false
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			mailbox.receive(connection)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		common.SMTPServer, common.SMTPPort = previousServer, previousPort
		common.SMTPAccount, common.SMTPFrom, common.SMTPToken = previousAccount, previousFrom, previousToken
		common.SMTPSSLEnabled, common.SMTPStartTLSEnabled = previousSSL, previousTLS
	})
	return mailbox
}

func (mailbox *securityMailbox) receive(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	client := textproto.NewConn(connection)
	if client.PrintfLine("220 localhost ESMTP") != nil {
		return
	}
	receiver := ""
	for {
		line, err := client.ReadLine()
		if err != nil {
			return
		}
		switch {
		case strings.HasPrefix(line, "RCPT TO:"):
			receiver = strings.TrimSuffix(strings.TrimPrefix(line, "RCPT TO:<"), ">")
		case line == "DATA":
			if client.PrintfLine("354 Send message") != nil {
				return
			}
			message, err := io.ReadAll(client.DotReader())
			if err != nil {
				return
			}
			mailbox.mutex.Lock()
			mailbox.mail[receiver] = append(mailbox.mail[receiver], string(message))
			mailbox.mutex.Unlock()
		case line == "QUIT":
			_ = client.PrintfLine("221 Goodbye")
			return
		}
		if client.PrintfLine("250 OK") != nil {
			return
		}
	}
}

func (mailbox *securityMailbox) code(t *testing.T, receiver string) string {
	t.Helper()
	mailbox.mutex.Lock()
	defer mailbox.mutex.Unlock()
	for index := len(mailbox.mail[receiver]) - 1; index >= 0; index-- {
		match := regexp.MustCompile(`<strong>([0-9]{6})</strong>`).FindStringSubmatch(mailbox.mail[receiver][index])
		if len(match) == 2 {
			return match[1]
		}
	}
	t.Fatalf("no verification email delivered to %s", receiver)
	return ""
}

func startSecurityEmailBinding(t *testing.T, identity service.AuthIdentity, email, method string) service.EmailBindingData {
	t.Helper()
	context, err := common.Marshal(service.AccountBindingContext{Provider: "email", Email: email})
	require.NoError(t, err)
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeAccountBind, Context: context}, method)
	request, err := common.Marshal(map[string]string{"email": email})
	require.NoError(t, err)
	response := securityEnrollmentRequest("POST", "/api/oauth/email/bind/start", string(request), proof, identity, EmailBindStart)
	var body securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success, response.Body.String())
	var flow service.EmailBindingData
	require.NoError(t, common.Unmarshal(body.Data, &flow))
	require.NotEmpty(t, flow.FlowToken)
	return flow
}

func TestSecurityAccountRequiresProofBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name, path, body string
		handler          gin.HandlerFunc
	}{
		{"password", "/api/user/self", `{"password":"account-password!42","original_password":"enrollment-password"}`, UpdateSelf},
		{"oauth binding", "/api/oauth/state", `{"provider":"security-account-test","intent":"bind"}`, GenerateOAuthCode},
	} {
		t.Run(test.name, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			oauth.Register("security-account-test", &authFlowTestOAuthProvider{})
			t.Cleanup(func() { oauth.Unregister("security-account-test") })
			response := securityEnrollmentRequest(http.MethodPost, test.path, test.body, "", identity, test.handler)
			var result securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, http.StatusForbidden, response.Code)
			assert.Equal(t, "SECURITY_PROOF_REQUIRED", result.Code)
			stored, err := model.GetUserById(user.Id, true)
			require.NoError(t, err)
			assert.True(t, common.ValidatePasswordAndHash("enrollment-password", stored.Password))
			var flows int64
			require.NoError(t, model.DB.Model(&model.AuthFlow{}).Count(&flows).Error)
			assert.Zero(t, flows)
		})
	}
}

func TestSecurityAccountPasswordRequiresCurrentPassword(t *testing.T) {
	for _, test := range []struct {
		name, original string
		success        bool
	}{
		{"missing", "", false},
		{"wrong", "wrong-password", false},
		{"correct", "enrollment-password", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopePasswordChange}, service.VerificationMethodPassword)
			request, err := common.Marshal(map[string]string{"password": "password123", "original_password": test.original})
			require.NoError(t, err)
			response := securityEnrollmentRequest(http.MethodPut, "/api/user/self", string(request), proof, identity, UpdateSelf)
			var result securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, test.success, result.Success, response.Body.String())
			stored, err := model.GetUserById(user.Id, true)
			require.NoError(t, err)
			if test.success {
				assert.True(t, common.ValidatePasswordAndHash("password123", stored.Password))
				assert.True(t, strings.HasPrefix(stored.Password, "$argon2id$"))
			} else {
				assert.Equal(t, "CURRENT_PASSWORD_INVALID", result.Code)
				assert.Equal(t, user.Password, stored.Password)
				repeated := securityEnrollmentRequest(http.MethodPut, "/api/user/self", string(request), proof, identity, UpdateSelf)
				require.NoError(t, common.Unmarshal(repeated.Body.Bytes(), &result))
				assert.Equal(t, "SECURITY_PROOF_CONSUMED", result.Code)
			}
		})
	}
}

func TestSecurityAccountProfileReadsPasswordStatusInOneQuery(t *testing.T) {
	for _, hasPassword := range []bool{true, false} {
		name := "password set"
		if !hasPassword {
			name = "password unset"
		}
		t.Run(name, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			updates := map[string]any{"access_token": "private-profile-token", "remark": "private-profile-remark"}
			if !hasPassword {
				updates["password"] = ""
			}
			require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Updates(updates).Error)
			queries := 0
			require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register("profile_query_count", func(tx *gorm.DB) {
				queries++
			}))
			profile, err := model.GetSelfUserById(user.Id)
			require.NoError(t, err)
			assert.Equal(t, 1, queries)
			assert.Equal(t, hasPassword, profile.HasPassword)
			assert.Empty(t, profile.Password)
			assert.Nil(t, profile.AccessToken)
			assert.Empty(t, profile.Remark)
			assert.Equal(t, user.AuthVersion, profile.AuthVersion)
			assert.Equal(t, user.Group, profile.Group)
			require.NoError(t, model.DB.Callback().Query().Remove("profile_query_count"))

			response := securityEnrollmentRequest(http.MethodGet, "/api/user/self", "", "", identity, GetSelf)
			var result struct {
				Success bool           `json:"success"`
				Data    map[string]any `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			require.True(t, result.Success, response.Body.String())
			assert.Equal(t, hasPassword, result.Data["has_password"])
			assert.NotContains(t, result.Data, "password")
			assert.NotContains(t, result.Data, "access_token")
			assert.NotContains(t, result.Data, "remark")

			bundle, err := service.CreateLoginSession(user.Id, "profile-test", "127.0.0.1", "profile-test")
			require.NoError(t, err)
			_, refreshed, err := service.RefreshLoginSession(bundle.RefreshToken, "", "127.0.0.1", "profile-test")
			require.NoError(t, err)
			assert.Equal(t, hasPassword, refreshed.HasPassword)
			assert.Empty(t, refreshed.Password)
			assert.Nil(t, refreshed.AccessToken)
		})
	}
}

func TestSecurityAccountProfileUpdateDoesNotRequireProof(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	response := securityEnrollmentRequest(http.MethodPut, "/api/user/self", `{"display_name":"Updated"}`, "", identity, UpdateSelf)
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, response.Body.String())
	stored, err := model.GetUserById(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "Updated", stored.DisplayName)
	assert.Equal(t, user.Password, stored.Password)
}

func TestSecurityAccountLongUnicodePasswordAndSessionRotation(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	other, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "other-device")
	require.NoError(t, err)
	otherIdentity, err := service.ParseAccessToken(other.AccessToken)
	require.NoError(t, err)
	password := strings.Repeat("安全🔒 ", 24)
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopePasswordChange}, service.VerificationMethodPassword)
	body, err := common.Marshal(map[string]string{"password": password, "original_password": "enrollment-password"})
	require.NoError(t, err)
	response := securityEnrollmentRequest(http.MethodPut, "/api/user/self", string(body), proof, identity, UpdateSelf)
	var result struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"access_token"`
			HasPassword bool   `json:"has_password"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, response.Body.String())
	assert.True(t, result.Data.HasPassword)
	currentIdentity, err := service.ParseAccessToken(result.Data.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(currentIdentity)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(otherIdentity)
	require.Error(t, err)
	login := model.User{Username: user.Username, Password: password}
	require.NoError(t, login.ValidateAndFill())
	assert.False(t, common.ValidatePasswordAndHash(strings.TrimSpace(password), login.Password))
	assert.False(t, common.ValidatePasswordAndHash("enrollment-password", login.Password))
}

func TestSecurityAccountPasswordPolicyAndHashCompatibility(t *testing.T) {
	t.Setenv("ACCOUNT_PASSWORD_HASH_ALGORITHM", "argon2id")
	for _, test := range []struct {
		name, password string
		valid          bool
	}{
		{"common password accepted", "password123", true},
		{"short", "seven77", false},
		{"too long", strings.Repeat("界", 129), false},
		{"long Unicode", strings.Repeat("界🔒 ", 40), true},
		{"whitespace preserved", "    phrase with whitespace    ", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			hash, err := common.HashAccountPassword(test.password)
			if !test.valid {
				require.Error(t, err)
				assert.Empty(t, hash)
				return
			}
			require.NoError(t, err)
			assert.True(t, common.ValidatePasswordAndHash(test.password, hash))
			assert.False(t, common.ValidatePasswordAndHash(test.password+"x", hash))
		})
	}
	legacy, err := common.Password2Hash("123456")
	require.NoError(t, err)
	assert.True(t, common.ValidatePasswordAndHash("123456", legacy), "login preserves historical passwords without applying new policy")
	for _, invalid := range []string{"$argon2id$", "$argon2id$v=19$m=4294967295,t=2,p=1$bad$bad", "$argon2id$v=19$m=19456,t=2,p=1$bad$bad"} {
		assert.False(t, common.ValidatePasswordAndHash("example-password", invalid))
	}
}

func TestSecurityAccountEncryptedLongPasswordLogin(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	password := strings.Repeat("🔒", 128)
	hash, err := common.HashAccountPassword(password)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(user).Update("password", hash).Error)
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
	key, nonce := make([]byte, 32), make([]byte, 12)
	_, err = rand.Read(key)
	require.NoError(t, err)
	_, err = rand.Read(nonce)
	require.NoError(t, err)
	wrappedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, key, []byte("password-v2"))
	require.NoError(t, err)
	aesBlock, err := aes.NewCipher(key)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(aesBlock)
	require.NoError(t, err)
	ciphertext := gcm.Seal(nil, nonce, []byte(password), []byte("password-v2:"+keyID))
	parts := []string{"v2", base64.StdEncoding.EncodeToString(wrappedKey), base64.StdEncoding.EncodeToString(nonce), base64.StdEncoding.EncodeToString(ciphertext)}
	encrypted := strings.Join(parts, ".")
	common.PasswordLoginEncryptionEnabled = true
	request, err := common.Marshal(LoginRequest{Username: user.Username, PasswordEncrypted: encrypted, EncryptionKeyID: keyID})
	require.NoError(t, err)
	response := securityEnrollmentRequest("POST", "/api/user/login", string(request), "", identity, Login)
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, response.Body.String())
	assert.Contains(t, string(result.Data), `"has_password":true`)
	proof, err := service.VerifySecurityInput(identity, service.VerificationInput{Method: "password", Scope: service.VerificationScopePasswordChange, PasswordEncrypted: encrypted, EncryptionKeyID: keyID})
	require.NoError(t, err)
	assert.NotEmpty(t, proof.ProofToken)
	ciphertext[len(ciphertext)-1] ^= 1
	parts[3] = base64.StdEncoding.EncodeToString(ciphertext)
	for _, invalid := range []string{strings.Join(parts, "."), "v2.bad.bad.bad", "v2." + strings.Repeat("a", 4096)} {
		_, err := common.DecryptPassword(invalid, keyID)
		assert.ErrorIs(t, err, common.ErrPasswordEncryptionInvalid)
	}
	_, err = common.DecryptPassword(encrypted, "wrong-key-id")
	assert.ErrorIs(t, err, common.ErrPasswordEncryptionInvalid)
}

func TestSecurityAccountEmailConfirmationAndAudit(t *testing.T) {
	for _, scenario := range []string{"first email", "replace without mfa", "replace with mfa"} {
		t.Run(scenario, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			mailbox := newSecurityMailbox(t)
			method, previous := service.VerificationMethodPassword, ""
			if scenario != "first email" {
				previous = "previous@example.com"
				require.NoError(t, model.DB.Model(user).Update("email", previous).Error)
			}
			if scenario == "replace with mfa" {
				require.NoError(t, model.DB.Create(&model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}).Error)
				method = service.VerificationMethodTwoFA
			}
			flow := startSecurityEmailBinding(t, identity, "New@Example.com", method)
			assert.Equal(t, "new@example.com", flow.Email)
			assert.Equal(t, scenario == "replace without mfa", flow.OldEmailRequired)
			newCode, oldCode := mailbox.code(t, flow.Email), ""
			_, state, err := model.GetEmailBinding(identity, flow.FlowToken)
			require.NoError(t, err)
			assert.NotEqual(t, newCode, state.NewCodeHash)
			assert.True(t, common.ValidatePasswordAndHash(newCode, state.NewCodeHash))
			if flow.OldEmailRequired {
				oldCode = mailbox.code(t, previous)
				_, err := service.FinishEmailBinding(identity, flow.FlowToken, newCode, "")
				assert.ErrorIs(t, err, model.ErrEmailBindingCodeInvalid)
				stored, err := model.GetUserById(user.Id, false)
				require.NoError(t, err)
				assert.Equal(t, previous, stored.Email)
			}
			request, err := common.Marshal(map[string]string{"flow_token": flow.FlowToken, "new_code": newCode, "old_code": oldCode})
			require.NoError(t, err)
			response := securityEnrollmentRequest("POST", "/api/oauth/email/bind", string(request), "", identity, EmailBind)
			var result securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			require.True(t, result.Success, response.Body.String())
			stored, err := model.GetUserById(user.Id, false)
			require.NoError(t, err)
			assert.Equal(t, flow.Email, stored.Email)
			_, err = service.FinishEmailBinding(identity, flow.FlowToken, newCode, oldCode)
			assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
			legacy := securityEnrollmentRequest("POST", "/api/oauth/email/bind", `{"email":"other@example.com","code":"123456"}`, "", identity, EmailBind)
			assert.Contains(t, legacy.Body.String(), `"success":false`)
			var audits []model.AuditLog
			require.NoError(t, model.LOG_DB.Find(&audits).Error)
			require.NotEmpty(t, audits)
			assert.False(t, audits[len(audits)-1].Success, "a rejected legacy request must be recorded as failed")
			encoded, err := common.Marshal(audits)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), "user.binding_bind")
			assert.NotContains(t, string(encoded), flow.FlowToken)
			assert.NotContains(t, string(encoded), newCode)
		})
	}
}

func TestSecurityAccountEmailResendAndAttemptLimit(t *testing.T) {
	_, identity := setupSecurityEnrollmentTest(t)
	mailbox := newSecurityMailbox(t)
	flow := startSecurityEmailBinding(t, identity, "new@example.com", service.VerificationMethodPassword)
	_, err := service.ResendAccountEmailBinding(identity, flow.FlowToken)
	assert.ErrorIs(t, err, model.ErrEmailBindingResendWait)
	_, err = service.FinishEmailBinding(identity, flow.FlowToken, "invalid", "")
	assert.ErrorIs(t, err, model.ErrEmailBindingCodeInvalid)
	stored, state, err := model.GetEmailBinding(identity, flow.FlowToken)
	require.NoError(t, err)
	oldHash := state.NewCodeHash
	state.ResendAt = time.Now().Add(-time.Second).Unix()
	payload, err := common.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(stored).Update("payload", string(payload)).Error)
	replacement, err := service.ResendAccountEmailBinding(identity, flow.FlowToken)
	require.NoError(t, err)
	assert.Equal(t, flow.ExpiresAt, replacement.ExpiresAt)
	_, state, err = model.GetEmailBinding(identity, flow.FlowToken)
	require.NoError(t, err)
	assert.Equal(t, 1, state.FailedAttempts)
	assert.NotEqual(t, oldHash, state.NewCodeHash)
	assert.True(t, common.ValidatePasswordAndHash(mailbox.code(t, flow.Email), state.NewCodeHash))
	for attempt := 2; attempt <= model.EmailBindingMaxAttempts; attempt++ {
		_, err = service.FinishEmailBinding(identity, flow.FlowToken, "invalid", "")
		if attempt < model.EmailBindingMaxAttempts {
			assert.ErrorIs(t, err, model.ErrEmailBindingCodeInvalid)
		} else {
			assert.ErrorIs(t, err, model.ErrEmailBindingLocked)
		}
	}
	_, err = service.FinishEmailBinding(identity, flow.FlowToken, mailbox.code(t, flow.Email), "")
	assert.ErrorIs(t, err, model.ErrEmailBindingLocked)
}

func TestSecurityAccountEmailConcurrentClaimsHaveOneOwner(t *testing.T) {
	user, firstIdentity := setupSecurityEnrollmentTest(t)
	mailbox := newSecurityMailbox(t)
	other := &model.User{Username: "other-user", AffCode: "other-account", Group: "default", Password: user.Password, Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1}
	require.NoError(t, model.DB.Create(other).Error)
	require.NoError(t, model.PublishUserAuthCache(other.Id))
	bundle, err := service.CreateLoginSession(other.Id, "password", "127.0.0.1", "other-user")
	require.NoError(t, err)
	secondIdentity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	start, results := make(chan struct{}), make(chan error, 2)
	for _, identity := range []service.AuthIdentity{firstIdentity, secondIdentity} {
		flow := startSecurityEmailBinding(t, identity, "shared@example.com", service.VerificationMethodPassword)
		code := mailbox.code(t, flow.Email)
		go func(identity service.AuthIdentity, token, code string) {
			<-start
			_, err := service.FinishEmailBinding(identity, token, code, "")
			results <- err
		}(identity, flow.FlowToken, code)
	}
	close(start)
	first, second := <-results, <-results
	assert.NotEqual(t, first == nil, second == nil, "only one account may claim the address: %v / %v", first, second)
	var owners int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("email = ?", "shared@example.com").Count(&owners).Error)
	assert.EqualValues(t, 1, owners)
}

func TestSecurityAccountEmailRejectsChangedAuthorization(t *testing.T) {
	for _, scenario := range []string{"other session", "expired", "email changed", "mfa enabled", "notification failure"} {
		t.Run(scenario, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			mailbox := newSecurityMailbox(t)
			flow := startSecurityEmailBinding(t, identity, "new@example.com", service.VerificationMethodPassword)
			code := mailbox.code(t, flow.Email)
			stored, _, err := model.GetEmailBinding(identity, flow.FlowToken)
			require.NoError(t, err)
			switch scenario {
			case "other session":
				identity.SessionID = "different-session"
			case "expired":
				require.NoError(t, model.DB.Model(stored).Update("expires_at", time.Now().Add(-time.Second)).Error)
			case "email changed":
				require.NoError(t, model.DB.Model(user).Update("email", "changed@example.com").Error)
			case "mfa enabled":
				require.NoError(t, model.DB.Create(&model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}).Error)
			case "notification failure":
				common.SMTPServer = ""
			}
			request, err := common.Marshal(map[string]string{"flow_token": flow.FlowToken, "new_code": code})
			require.NoError(t, err)
			response := securityEnrollmentRequest("POST", "/api/oauth/email/bind", string(request), "", identity, EmailBind)
			var result securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, scenario == "notification failure", result.Success, response.Body.String())
			if scenario == "notification failure" {
				assert.Contains(t, string(result.Data), `"notification_warning":true`)
			}
			current, err := model.GetUserById(user.Id, false)
			require.NoError(t, err)
			assert.Equal(t, scenario == "notification failure", current.Email == flow.Email)
		})
	}
}

func TestSecurityAccountOAuthBindingRejectsInvalidFlow(t *testing.T) {
	for _, scenario := range []string{"success and replay", "other session", "wrong provider", "expired", "old flow", "mfa enabled"} {
		t.Run(scenario, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			oauth.Register("account-oauth", &enrollmentOAuthProvider{externalID: "new-binding"})
			oauth.Register("other-oauth", &enrollmentOAuthProvider{externalID: "wrong-binding"})
			t.Cleanup(func() { oauth.Unregister("account-oauth"); oauth.Unregister("other-oauth") })
			operation := service.VerificationOperation{Scope: service.VerificationScopeAccountBind, Context: []byte(`{"provider":"account-oauth"}`)}
			proof := issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
			response := securityEnrollmentRequest("POST", "/api/oauth/state", `{"provider":"account-oauth","intent":"bind"}`, proof, identity, GenerateOAuthCode)
			var result securityEnrollmentResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			require.True(t, result.Success, response.Body.String())
			var started struct {
				FlowToken string `json:"flow_token"`
			}
			require.NoError(t, common.Unmarshal(result.Data, &started))
			flow, err := model.GetAuthFlow(started.FlowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
			require.NoError(t, err)
			provider := "account-oauth"
			switch scenario {
			case "other session":
				identity.SessionID = "different-session"
			case "wrong provider":
				provider = "other-oauth"
			case "expired":
				require.NoError(t, model.DB.Model(flow).Update("expires_at", time.Now().Add(-time.Second)).Error)
			case "old flow":
				require.NoError(t, model.DB.Model(flow).Update("payload", "{}").Error)
			case "mfa enabled":
				require.NoError(t, model.DB.Create(&model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}).Error)
			}
			handler := func(c *gin.Context) { c.Params = gin.Params{{Key: "provider", Value: provider}}; HandleOAuth(c) }
			path := "/api/oauth/" + provider + "?state=" + started.FlowToken + "&code=provider-code"
			response = securityEnrollmentRequest("GET", path, "", "", identity, handler)
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, scenario == "success and replay", result.Success, response.Body.String())
			current, err := model.GetUserById(user.Id, false)
			require.NoError(t, err)
			assert.Equal(t, scenario == "success and replay", current.GitHubId == "new-binding")
			if result.Success {
				replay := securityEnrollmentRequest("GET", path, "", "", identity, handler)
				assert.Contains(t, replay.Body.String(), `"success":false`)
			}
		})
	}
}

func TestSecurityAccountFirstPasswordRace(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.Model(user).Update("password", "").Error)
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id, CredentialID: "existing-key", PublicKey: "public-key"}).Error)
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopePasswordSet}, service.VerificationMethodPasskey)
	request := `{"password":"initial-account-password!42"}`
	response := securityEnrollmentRequest("PUT", "/api/user/self", request, proof, identity, UpdateSelf)
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, response.Body.String())
	user, identity = setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.Model(user).Update("password", "").Error)
	start, results := make(chan struct{}), make(chan error, 2)
	for _, password := range []string{"first-password!42", "second-password!42"} {
		go func(password string) {
			<-start
			results <- model.ChangeUserPassword(identity, &model.User{Id: user.Id, Password: password}, true)
		}(password)
	}
	close(start)
	first, second := <-results, <-results
	assert.NotEqual(t, first == nil, second == nil, "only one concurrent first password may succeed: %v / %v", first, second)
	stored, err := model.GetUserById(user.Id, true)
	require.NoError(t, err)
	assert.True(t, common.ValidatePasswordAndHash("first-password!42", stored.Password) || common.ValidatePasswordAndHash("second-password!42", stored.Password))
}

func TestSecurityAccountUnbindPreservesUsableLoginMethod(t *testing.T) {
	for _, scenario := range []string{"password", "passkey", "email only", "twofa only", "disabled password", "disabled passkey"} {
		t.Run(scenario, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			previousPasswordEnabled := common.PasswordLoginEnabled
			t.Cleanup(func() { common.PasswordLoginEnabled = previousPasswordEnabled })
			common.PasswordLoginEnabled = scenario != "disabled password"
			require.NoError(t, model.DB.Create(&model.UserOAuthBinding{UserId: user.Id, ProviderId: 31, ProviderUserId: "linked-subject"}).Error)
			if scenario != "password" && scenario != "disabled password" {
				require.NoError(t, model.DB.Model(user).Updates(map[string]any{"password": "", "email": "verified@example.com"}).Error)
			}
			if scenario == "passkey" || scenario == "disabled passkey" {
				require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: user.Id, CredentialID: "existing-key", PublicKey: "public-key"}).Error)
				system_setting.GetPasskeySettings().Enabled = scenario == "passkey"
			}
			if scenario == "twofa only" {
				require.NoError(t, model.DB.Create(&model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}).Error)
			}
			err := service.UnbindAccountOAuth(identity, 31)
			if scenario == "password" || scenario == "passkey" {
				require.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, model.ErrLastLoginMethod)
				var binding model.UserOAuthBinding
				require.NoError(t, model.DB.Where("user_id = ? AND provider_id = ?", user.Id, 31).First(&binding).Error)
			}
		})
	}
}
