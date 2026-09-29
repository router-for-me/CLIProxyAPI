package middleware

import (
	"log"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func ConfigureTrustedProxies(engine *gin.Engine) error {
	trustedProxies, usedDefaults, err := common.ResolveTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return err
	}
	if usedDefaults {
		log.Print("WARNING: TRUSTED_PROXIES is unset or blank; trusting loopback, RFC 1918, and IPv6 ULA proxy addresses for compatibility. Set TRUSTED_PROXIES=none to trust no proxies, or configure explicit proxy IPs/CIDRs to replace these defaults.")
	}
	return common.ConfigureTrustedProxies(engine, trustedProxies)
}
