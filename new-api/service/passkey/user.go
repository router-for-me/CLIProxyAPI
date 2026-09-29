package passkey

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/model"

	webauthn "github.com/go-webauthn/webauthn/webauthn"
)

type WebAuthnUser struct {
	user       *model.User
	credential *model.PasskeyCredential
}

func NewWebAuthnUser(user *model.User, credential *model.PasskeyCredential) *WebAuthnUser {
	return &WebAuthnUser{user: user, credential: credential}
}

func (u *WebAuthnUser) WebAuthnID() []byte {
	if u == nil || u.user == nil {
		return nil
	}
	return []byte(strconv.Itoa(u.user.Id))
}

func (u *WebAuthnUser) WebAuthnName() string {
	if u == nil || u.user == nil {
		return ""
	}
	name := strings.TrimSpace(u.user.Username)
	if name == "" {
		return fmt.Sprintf("user-%d", u.user.Id)
	}
	return name
}

func (u *WebAuthnUser) WebAuthnDisplayName() string {
	if u == nil || u.user == nil {
		return ""
	}
	display := strings.TrimSpace(u.user.DisplayName)
	if display != "" {
		return display
	}
	return u.WebAuthnName()
}

func (u *WebAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	if u == nil || u.credential == nil {
		return nil
	}
	cred := u.credential.ToWebAuthnCredential()
	return []webauthn.Credential{cred}
}

func (u *WebAuthnUser) ModelUser() *model.User {
	if u == nil {
		return nil
	}
	return u.user
}

func (u *WebAuthnUser) PasskeyCredential() *model.PasskeyCredential {
	if u == nil {
		return nil
	}
	return u.credential
}
