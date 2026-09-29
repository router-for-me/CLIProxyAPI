package service

import (
	"crypto/rand"
	"errors"
	"fmt"
	"html"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

var (
	ErrAccountEmailInvalid    = errors.New("Please enter a valid email address")
	ErrAccountEmailRestricted = errors.New("This email address is not allowed by the administrator's email policy.")
	ErrEmailBindingDelivery   = errors.New("Verification email could not be sent. Start email verification again.")
)

type EmailBindingData struct {
	FlowToken           string `json:"flow_token"`
	Email               string `json:"email"`
	CurrentEmail        string `json:"current_email,omitempty"`
	OldEmailRequired    bool   `json:"old_email_required"`
	ExpiresAt           int64  `json:"expires_at"`
	ResendAt            int64  `json:"resend_at"`
	NotificationWarning bool   `json:"notification_warning"`
}

// ValidateAccountEmail is shared with registration mail delivery so binding
// cannot bypass the site's existing domain, alias and address-length rules.
func ValidateAccountEmail(email string) (string, error) {
	email = model.NormalizeEmail(email)
	if common.Validate.Var(email, "required,email,max=50") != nil {
		return "", ErrAccountEmailInvalid
	}
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return "", ErrAccountEmailInvalid
	}
	if common.EmailDomainRestrictionEnabled {
		allowed := slices.Contains(common.EmailDomainWhitelist, parts[1])
		if !allowed {
			return "", ErrAccountEmailRestricted
		}
	}
	if common.EmailAliasRestrictionEnabled && strings.ContainsAny(parts[0], "+.") {
		return "", ErrAccountEmailRestricted
	}
	return email, nil
}

func StartEmailBinding(identity AuthIdentity, authorization *model.AuthFlowAuthorization, email string) (*EmailBindingData, error) {
	email, err := ValidateAccountEmail(email)
	if err != nil {
		return nil, err
	}
	context, err := common.Marshal(AccountBindingContext{Provider: "email", Email: email})
	if err != nil {
		return nil, err
	}
	if err := ValidateFlowAuthorization(identity, VerificationOperation{Scope: VerificationScopeAccountBind, Context: context}, authorization); err != nil {
		return nil, err
	}
	user, err := model.GetUserById(identity.UserID, false)
	if err != nil {
		return nil, err
	}
	state := model.EmailBindingState{Authorization: authorization, CurrentEmail: model.NormalizeEmail(user.Email), Email: email, ResendAt: time.Now().Add(model.EmailBindingResendDelay).Unix()}
	requireOld := state.CurrentEmail != "" && authorization.Method != VerificationMethodTwoFA && authorization.Method != VerificationMethodPasskey
	codes, err := generateEmailBindingCodes(requireOld)
	if err != nil {
		return nil, err
	}
	state.NewCodeHash, state.OldCodeHash = codes.NewHash, codes.OldHash
	token, flow, err := model.CreateEmailBinding(identity, state)
	if err != nil {
		return nil, err
	}
	if err := sendEmailBindingCodes(state, codes); err != nil {
		// A partially delivered pair must not leave a usable change request.
		_, _ = model.ConsumeAuthFlow(token, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeEmailBinding, UserId: identity.UserID, SessionId: identity.SessionID})
		return nil, ErrEmailBindingDelivery
	}
	data := emailBindingData(token, flow, &state)
	if state.CurrentEmail != "" && !requireOld {
		data.NotificationWarning = NotifyAccountSecurityChange(state.CurrentEmail, "A change of your email address was requested") != nil
	}
	return data, nil
}

func ResendAccountEmailBinding(identity AuthIdentity, token string) (*EmailBindingData, error) {
	_, state, err := model.GetEmailBinding(identity, token)
	if err != nil {
		return nil, err
	}
	context, err := common.Marshal(AccountBindingContext{Provider: "email", Email: state.Email})
	if err != nil {
		return nil, err
	}
	if err := ValidateFlowAuthorization(identity, VerificationOperation{Scope: VerificationScopeAccountBind, Context: context}, state.Authorization); err != nil {
		return nil, err
	}
	if state.ResendAt > time.Now().Unix() {
		return nil, model.ErrEmailBindingResendWait
	}
	if state.FailedAttempts >= model.EmailBindingMaxAttempts {
		return nil, model.ErrEmailBindingLocked
	}
	codes, err := generateEmailBindingCodes(state.OldCodeHash != "")
	if err != nil {
		return nil, err
	}
	flow, state, err := model.ResendEmailBinding(identity, token, codes.NewHash, codes.OldHash)
	if err != nil {
		return nil, err
	}
	if err := sendEmailBindingCodes(*state, codes); err != nil {
		_, _ = model.ConsumeAuthFlow(token, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeEmailBinding, UserId: identity.UserID, SessionId: identity.SessionID})
		return nil, ErrEmailBindingDelivery
	}
	return emailBindingData(token, flow, state), nil
}

func FinishEmailBinding(identity AuthIdentity, token, newCode, oldCode string) (*model.EmailBindingState, error) {
	_, state, err := model.GetEmailBinding(identity, token)
	if err != nil {
		return nil, err
	}
	context, err := common.Marshal(AccountBindingContext{Provider: "email", Email: state.Email})
	if err != nil {
		return nil, err
	}
	if err := ValidateFlowAuthorization(identity, VerificationOperation{Scope: VerificationScopeAccountBind, Context: context}, state.Authorization); err != nil {
		return nil, err
	}
	if _, err := ValidateAccountEmail(state.Email); err != nil {
		return nil, err
	}
	return model.CompleteEmailBinding(identity, token, state.Email, newCode, oldCode)
}

type emailBindingCodes struct {
	New, Old         string
	NewHash, OldHash string
}

func generateEmailBindingCodes(requireOld bool) (emailBindingCodes, error) {
	var codes emailBindingCodes
	count := 1
	if requireOld {
		count = 2
	}
	for index := 0; index < count; {
		number, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return codes, err
		}
		code := fmt.Sprintf("%06d", number.Int64())
		if code == codes.New {
			continue
		}
		hash, err := common.Password2Hash(code)
		if err != nil {
			return codes, err
		}
		if index == 0 {
			codes.New, codes.NewHash = code, hash
		} else {
			codes.Old, codes.OldHash = code, hash
		}
		index++
	}
	return codes, nil
}

func sendEmailBindingCodes(state model.EmailBindingState, codes emailBindingCodes) error {
	subject := common.SystemName + " — Confirm your email address"
	content := fmt.Sprintf("<p>Confirm linking this email address to your account.</p><p>Verification code: <strong>%s</strong></p><p>This code expires in 10 minutes. If you did not request this change, do not share this code.</p>", html.EscapeString(codes.New))
	if err := common.SendEmail(subject, state.Email, content); err != nil {
		return err
	}
	if codes.Old != "" {
		content = fmt.Sprintf("<p>A change to your account email address was requested. Confirm replacing your current address.</p><p>Verification code: <strong>%s</strong></p><p>This code expires in 10 minutes. If you did not request this change, do not share this code and contact your administrator.</p>", html.EscapeString(codes.Old))
		return common.SendEmail(subject, state.CurrentEmail, content)
	}
	return nil
}

func emailBindingData(token string, flow *model.AuthFlow, state *model.EmailBindingState) *EmailBindingData {
	return &EmailBindingData{
		FlowToken: token, Email: state.Email, CurrentEmail: common.MaskEmail(state.CurrentEmail),
		OldEmailRequired: state.OldCodeHash != "", ExpiresAt: flow.ExpiresAt.Unix(), ResendAt: state.ResendAt,
	}
}
