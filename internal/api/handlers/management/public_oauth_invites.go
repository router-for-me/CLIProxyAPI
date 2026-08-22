package management

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usercontrol"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const oauthInviteTokenContextKey = "oauth-invite-token"

func (h *Handler) GetPublicOAuthInvite(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	invite, err := service.GetOAuthInvite(c.Request.Context(), c.Param("token"))
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"label": invite.Label, "providers": invite.Providers,
		"remaining_uses": invite.MaxUses - invite.UsedUses - invite.ReservedUses,
		"expires_at":     invite.ExpiresAt,
	})
}

func (h *Handler) StartPublicOAuthInvite(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	token := strings.TrimSpace(c.Param("token"))
	if _, err := service.GetOAuthInvite(c.Request.Context(), token); err != nil {
		writeUserControlError(c, err)
		return
	}
	var request struct {
		Provider string `json:"provider"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider payload"})
		return
	}
	c.Set(oauthInviteTokenContextKey, token)
	c.Header("Cache-Control", "no-store")
	switch strings.ToLower(strings.TrimSpace(request.Provider)) {
	case "anthropic":
		h.RequestAnthropicToken(c)
	case "codex":
		h.RequestCodexToken(c)
	case "antigravity":
		h.RequestAntigravityToken(c)
	case "kimi":
		h.RequestKimiToken(c)
	case "xai":
		h.RequestXAIToken(c)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported OAuth provider"})
	}
}

func (h *Handler) GetPublicOAuthInviteStatus(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "state is required"})
		return
	}
	owns, err := service.OAuthInviteOwnsSession(c.Request.Context(), c.Param("token"), state)
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	if !owns {
		c.JSON(http.StatusNotFound, gin.H{"error": "OAuth session not found"})
		return
	}
	c.Header("Cache-Control", "no-store")
	h.GetAuthStatus(c)
}

func (h *Handler) PostPublicOAuthInviteCallback(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	var request oauthCallbackRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid callback payload"})
		return
	}
	state := callbackRequestState(request)
	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "state is required"})
		return
	}
	owns, err := service.OAuthInviteOwnsSession(c.Request.Context(), c.Param("token"), state)
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	if !owns {
		c.JSON(http.StatusNotFound, gin.H{"error": "OAuth session not found"})
		return
	}
	h.handleOAuthCallback(c, request)
}

func (h *Handler) ServePublicOAuthInvite(c *gin.Context) {
	if _, ok := h.requireUserControl(c); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(oauthInviteHTML))
}

func (h *Handler) reserveOAuthInvite(c *gin.Context, state, provider string) bool {
	rawToken, exists := c.Get(oauthInviteTokenContextKey)
	if !exists {
		return true
	}
	token, _ := rawToken.(string)
	h.mu.Lock()
	service := h.userControl
	h.mu.Unlock()
	if service == nil {
		CancelOAuthSession(state)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth invitations are unavailable"})
		return false
	}
	if _, err := service.ReserveOAuthInvite(c.Request.Context(), token, state, provider); err != nil {
		CancelOAuthSession(state)
		writeUserControlError(c, err)
		return false
	}
	return true
}

// completeOAuthContribution turns a temporary reservation into a consumed invitation.
// Normal management logins have no reservation, so ErrNotFound is expected there.
func (h *Handler) completeOAuthContribution(ctx context.Context, state string, record *coreauth.Auth) {
	h.mu.Lock()
	service := h.userControl
	h.mu.Unlock()
	if service == nil || record == nil {
		return
	}
	email := ""
	if record.Metadata != nil {
		email, _ = record.Metadata["email"].(string)
	}
	if err := service.CompleteOAuthInvite(ctx, state, record.ID, email); err != nil && !errors.Is(err, usercontrol.ErrNotFound) {
		log.WithError(err).WithField("state", state).Warn("failed to complete OAuth invitation contribution")
	}
}

func callbackRequestState(request oauthCallbackRequest) string {
	if state := strings.TrimSpace(request.State); state != "" {
		return state
	}
	parsed, err := url.Parse(strings.TrimSpace(request.RedirectURL))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("state"))
}

const oauthInviteHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Contribute an OAuth login</title><style>
:root{color-scheme:dark;font-family:Inter,system-ui,sans-serif;background:#0b1020;color:#e8ecf6}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;box-sizing:border-box}.card{width:min(620px,100%);background:#141b2f;border:1px solid #27314d;border-radius:18px;padding:28px;box-shadow:0 24px 70px #0008}h1{margin:0 0 10px;font-size:28px}p{color:#aeb9d5;line-height:1.55}.providers{display:grid;gap:10px;margin:22px 0}button,a.action{border:0;border-radius:10px;background:#6d7cff;color:white;padding:12px 16px;font-weight:700;cursor:pointer;text-decoration:none;text-align:center}button:disabled{opacity:.5;cursor:not-allowed}.provider{display:flex;justify-content:space-between;align-items:center;background:#0e1528;border:1px solid #27314d;border-radius:12px;padding:13px}.status{padding:12px;border-radius:10px;background:#0e1528;min-height:22px}.callback{display:none;margin-top:16px}textarea{width:100%;min-height:90px;box-sizing:border-box;border:1px solid #39476d;border-radius:10px;background:#080d19;color:#fff;padding:11px;margin:8px 0 10px}.small{font-size:13px}
</style></head><body><main class="card"><h1>Contribute an OAuth login</h1><p id="intro">Loading invitation…</p><div id="providers" class="providers"></div><div id="status" class="status">Waiting.</div><section id="callback" class="callback"><p class="small">If the provider redirects to localhost and the page does not close automatically, paste the complete redirected URL here.</p><textarea id="redirect" placeholder="http://localhost:.../?code=...&state=..."></textarea><button id="submitCallback">Send callback</button></section><p class="small">The server stores the OAuth credential in its normal OAuth Login list. This page never displays the credential.</p></main><script>
const token=location.pathname.split('/').pop(),base='/v0/oauth/invites/'+encodeURIComponent(token);let state='',provider='',timer;
const statusEl=document.getElementById('status'),providersEl=document.getElementById('providers'),callbackEl=document.getElementById('callback');
async function json(url,options){const response=await fetch(url,options);const body=await response.json().catch(()=>({}));if(!response.ok)throw new Error(body.error||'Request failed');return body}
async function load(){try{const invite=await json(base);document.getElementById('intro').textContent=invite.label+' · '+invite.remaining_uses+' use(s) remaining';invite.providers.forEach(name=>{const row=document.createElement('div');row.className='provider';const label=document.createElement('span');label.textContent=name;const button=document.createElement('button');button.textContent='Authorize';button.onclick=()=>start(name,button);row.append(label,button);providersEl.append(row)})}catch(error){statusEl.textContent=error.message}}
async function start(name,button){try{document.querySelectorAll('button').forEach(item=>item.disabled=true);provider=name;const data=await json(base+'/start',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({provider:name})});state=data.state;statusEl.textContent=data.user_code?'Open the provider and enter code '+data.user_code:'Authorization opened in a new tab.';window.open(data.url,'_blank','noopener');callbackEl.style.display=data.flow==='device'?'none':'block';timer=setInterval(poll,1500)}catch(error){statusEl.textContent=error.message;button.disabled=false}}
async function poll(){try{const data=await json(base+'/status?state='+encodeURIComponent(state));if(data.status==='ok'){clearInterval(timer);statusEl.textContent='Done. The OAuth login was saved.';callbackEl.style.display='none'}else if(data.status==='error'){clearInterval(timer);statusEl.textContent=data.error||'Authorization failed.'}}catch(error){clearInterval(timer);statusEl.textContent=error.message}}
document.getElementById('submitCallback').onclick=async()=>{try{await json(base+'/callback',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({provider,state,redirect_url:document.getElementById('redirect').value})});statusEl.textContent='Callback received. Finishing authorization…'}catch(error){statusEl.textContent=error.message}};load();
</script></body></html>`
