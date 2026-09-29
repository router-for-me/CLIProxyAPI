package model

import (
	"crypto/hmac"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var passkeyOptionMutex sync.Mutex
var errPasskeyDomainPreview = errors.New("passkey domain preview")
var ErrPasskeyDomainRemovalConfirmation = errors.New("Review the affected Passkeys and confirm the domain removal.")

// PasskeyDomainChange is both the preview and the authoritative saved result.
// Unknown credentials may belong to a removed domain; they are not a known loss.
type PasskeyDomainChange struct {
	RPID                 string   `json:"rp_id"`
	LegacyRPIDs          string   `json:"legacy_rp_ids"`
	Origins              string   `json:"origins"`
	PreviousRPID         string   `json:"previous_rp_id"`
	EffectiveRPID        string   `json:"effective_rp_id"`
	RemovedRPIDs         []string `json:"removed_rp_ids"`
	AffectedCredentials  int64    `json:"affected_credentials"`
	UnknownCredentials   int64    `json:"unknown_credentials"`
	ConfirmationRequired bool     `json:"confirmation_required"`
	RemovalConfirmation  string   `json:"removal_confirmation"`
}

type PasskeyDomainRemovalError struct {
	Change *PasskeyDomainChange
}

func (e *PasskeyDomainRemovalError) Error() string {
	return ErrPasskeyDomainRemovalConfirmation.Error()
}
func (e *PasskeyDomainRemovalError) Unwrap() error { return ErrPasskeyDomainRemovalConfirmation }

// ServerAddress and origins participate because either can change an implicit RP ID.
func IsPasskeyDomainOption(key string) bool {
	return key == "passkey.rp_id" || key == "passkey.legacy_rp_ids" || key == "passkey.origins" || key == "ServerAddress"
}

// lockPasskeyDomainSettings serializes trust changes and credential writes across
// nodes. Callers also hold passkeyOptionMutex for SQLite and local cache ordering.
// Always acquire these option rows before any user/session/credential row locks.
func lockPasskeyDomainSettings(tx *gorm.DB) (system_setting.PasskeySettings, string, error) {
	common.OptionMapRWMutex.RLock()
	settings, serverAddress := *system_setting.GetPasskeySettings(), system_setting.ServerAddress
	common.OptionMapRWMutex.RUnlock()
	rows := []Option{
		{Key: "ServerAddress", Value: serverAddress},
		{Key: "passkey.legacy_rp_ids", Value: settings.LegacyRPIDs},
		{Key: "passkey.origins", Value: settings.Origins},
		{Key: "passkey.rp_id", Value: settings.RPID},
	}
	// The first write also acquires SQLite's writer lock before any reads.
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
		return settings, serverAddress, err
	}
	for i := range rows {
		if err := lockForUpdate(tx).Where(&Option{Key: rows[i].Key}).First(&rows[i]).Error; err != nil {
			return settings, serverAddress, err
		}
	}
	serverAddress = rows[0].Value
	settings.LegacyRPIDs, settings.Origins, settings.RPID = rows[1].Value, rows[2].Value, rows[3].Value
	return settings, serverAddress, nil
}

func validatePasskeyRPIDWithTx(tx *gorm.DB, rpID string) error {
	settings, serverAddress, err := lockPasskeyDomainSettings(tx)
	if err != nil {
		return err
	}
	// Preserve the existing request-derived default only when no domain or
	// origin has ever been configured. Finish still checks the signed origin.
	if settings.RPID == "" && settings.LegacyRPIDs == "" && settings.Origins == "" && serverAddress == "" {
		return nil
	}
	if !slices.Contains(settings.WithDefaults(serverAddress).RelyingPartyIDs(), rpID) {
		return system_setting.ErrPasskeyRPIDUnavailable
	}
	return nil
}

// UpdatePasskeyDomainOptions keeps every supplied option in one transaction,
// including unrelated keys from UpdateOptionsBulk. Preview rolls back even the
// initial default rows and never publishes a local configuration change.
func UpdatePasskeyDomainOptions(values map[string]string, preview bool, confirmation string) (*PasskeyDomainChange, error) {
	passkeyOptionMutex.Lock()
	defer passkeyOptionMutex.Unlock()
	values = maps.Clone(values)
	for key, value := range values {
		if err := validateOptionValue(key, value); err != nil {
			return nil, err
		}
	}
	var change *PasskeyDomainChange
	err := DB.Transaction(func(tx *gorm.DB) error {
		settings, serverAddress, err := lockPasskeyDomainSettings(tx)
		if err != nil {
			return err
		}
		previous := settings.WithDefaults(serverAddress)
		before := [4]string{serverAddress, settings.RPID, settings.LegacyRPIDs, settings.Origins}
		if value, changed := values["ServerAddress"]; changed {
			serverAddress = value
		}
		if value, changed := values["passkey.origins"]; changed {
			origins := []string{}
			for _, origin := range strings.FieldsFunc(value, func(c rune) bool { return c == ',' || c == '\n' || c == '\r' }) {
				origin = strings.TrimSpace(origin)
				if origin != "" && !slices.Contains(origins, origin) {
					origins = append(origins, origin)
				}
			}
			settings.Origins = strings.Join(origins, ",")
		} else if before[0] != serverAddress && (settings.Origins == "" || settings.Origins == "[]") {
			// Retain the previously trusted implicit website as well as the new
			// default, so retaining its RP ID does not leave its keys unusable.
			origins := []string{}
			for _, origin := range []string{previous.Origins, settings.WithDefaults(serverAddress).Origins} {
				if origin != "" && !slices.Contains(origins, origin) {
					origins = append(origins, origin)
				}
			}
			settings.Origins = strings.Join(origins, ",")
		}
		if value, changed := values["passkey.rp_id"]; changed {
			settings.RPID = strings.TrimSpace(value)
			if settings.RPID != "" {
				settings.RPID, err = system_setting.NormalizePasskeyRPID(settings.RPID, settings.WithDefaults(serverAddress).Origins)
				if err != nil {
					return err
				}
			}
		}
		if value, changed := values["passkey.legacy_rp_ids"]; changed {
			settings.LegacyRPIDs = value
		}
		effective := settings.WithDefaults(serverAddress)
		legacy, err := system_setting.ParsePasskeyRPIDs(settings.LegacyRPIDs, effective.Origins)
		if err != nil {
			return err
		}
		oldRPID, nextRPID := previous.EffectiveRPID(), effective.EffectiveRPID()
		_, previousDomainError := system_setting.NormalizePasskeyRPID(oldRPID, previous.Origins)
		if previousDomainError == nil && oldRPID != nextRPID && !slices.Contains(legacy, oldRPID) {
			legacy = append(legacy, oldRPID)
		}
		settings.LegacyRPIDs = strings.Join(legacy, ",")
		// Validate retained single-label domains against the complete proposed
		// origin set too. Do not normalize their historical hash inputs.
		if _, err := system_setting.ParsePasskeyRPIDs(settings.LegacyRPIDs, effective.Origins); err != nil {
			return err
		}
		// ServerAddress also supports non-WebAuthn deployments (for example an
		// IP during initial setup). Restrict explicit RP IDs, not that URL.
		if settings.RPID != "" {
			if _, err := system_setting.NormalizePasskeyRPID(nextRPID, effective.Origins); err != nil {
				return err
			}
		}
		change = &PasskeyDomainChange{
			RPID: settings.RPID, LegacyRPIDs: settings.LegacyRPIDs, Origins: settings.Origins,
			PreviousRPID: oldRPID, EffectiveRPID: nextRPID, RemovedRPIDs: []string{},
		}
		nextIDs := settings.WithDefaults(serverAddress).RelyingPartyIDs()
		for _, id := range previous.RelyingPartyIDs() {
			if !slices.Contains(nextIDs, id) {
				change.RemovedRPIDs = append(change.RemovedRPIDs, id)
			}
		}
		if len(change.RemovedRPIDs) > 0 {
			// MySQL may compare/group text case-insensitively. Select only
			// candidate RP values, then count exact bytes without credential IDs.
			rows, err := tx.Model(&PasskeyCredential{}).Select("rp_id").
				Where("rp_id IS NULL OR rp_id = ? OR rp_id IN ?", "", change.RemovedRPIDs).Rows()
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var rpID *string
				if err := rows.Scan(&rpID); err != nil {
					return err
				}
				if rpID == nil || *rpID == "" {
					change.UnknownCredentials++
				} else if slices.Contains(change.RemovedRPIDs, *rpID) {
					change.AffectedCredentials++
				}
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
		change.ConfirmationRequired = change.AffectedCredentials > 0 || change.UnknownCredentials > 0
		// This is an intent/freshness binding, not an authentication credential.
		// Root authorization remains mandatory. No map iteration affects its hash.
		payload, err := common.Marshal(struct {
			Before         [4]string
			After          [4]string
			Removed        []string
			Known, Unknown int64
		}{before, [4]string{serverAddress, settings.RPID, settings.LegacyRPIDs, settings.Origins}, change.RemovedRPIDs, change.AffectedCredentials, change.UnknownCredentials})
		if err != nil {
			return err
		}
		change.RemovalConfirmation = common.GenerateHMACWithKey([]byte("passkey-domains-v1:"+common.SessionSecret), string(payload))
		if preview {
			return errPasskeyDomainPreview
		}
		if (change.ConfirmationRequired || confirmation != "") && !hmac.Equal([]byte(confirmation), []byte(change.RemovalConfirmation)) {
			return &PasskeyDomainRemovalError{Change: change}
		}
		values["passkey.rp_id"], values["passkey.legacy_rp_ids"], values["passkey.origins"] = settings.RPID, settings.LegacyRPIDs, settings.Origins
		values["ServerAddress"] = serverAddress
		keys := slices.Sorted(maps.Keys(values))
		for _, key := range keys {
			if err := tx.Save(&Option{Key: key, Value: values[key]}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errPasskeyDomainPreview) {
		return change, nil
	}
	if err != nil {
		return change, err
	}
	applyPasskeyDomainOptions(values)
	for key, value := range values {
		if !IsPasskeyDomainOption(key) {
			if err := updateOptionMap(key, value); err != nil {
				return change, err
			}
		}
	}
	return change, nil
}

func applyPasskeyDomainOptions(values map[string]string) {
	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	for _, key := range []string{"ServerAddress", "passkey.legacy_rp_ids", "passkey.origins", "passkey.rp_id"} {
		if value, ok := values[key]; ok {
			common.OptionMap[key] = value
			if key == "ServerAddress" {
				system_setting.ServerAddress = value
			} else {
				handleConfigUpdate(key, value)
			}
		}
	}
}
