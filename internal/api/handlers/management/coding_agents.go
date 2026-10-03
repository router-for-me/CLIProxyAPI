package management

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Coding-agent auto-configuration. The actual file transforms live in
// bin/cli-agents(.exe) (compiled from bin/configure_agents.js — ONE source of
// truth); this handler is a thin, validated passthrough so the Management UI
// can drive it. The exe is expected next to config.yaml, like quota.json.

var agentNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// First char must be alphanumeric so a stamp can never masquerade as an exe flag.
var agentStampRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// agentsMu serializes exe runs: two concurrent apply/revert requests must not
// mutate the same agent config files and backup stamps.
var agentsMu sync.Mutex

func (h *Handler) codingAgentsExe() string {
	name := "cli-agents"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(h.configFilePath), name)
}

// runAgentsExe executes the companion exe and returns its exit code + output.
// Argv-style exec (no shell) plus regex-validated inputs keeps this safe even
// though the values originate from a management-authenticated request.
func (h *Handler) runAgentsExe(args ...string) (int, string) {
	agentsMu.Lock()
	defer agentsMu.Unlock()
	exe := h.codingAgentsExe()
	if _, err := os.Stat(exe); err != nil {
		return http.StatusInternalServerError, `{"error":"exe_missing","message":"cli-agents executable not found next to config.yaml"}`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	out, err := cmd.CombinedOutput()
	// Check err before the deadline: a run that finished successfully just as
	// the 60s limit fired already mutated config files and must report 200,
	// not a retry-bait 504.
	if err != nil {
		if ctx.Err() != nil {
			return http.StatusGatewayTimeout, `{"error":"timeout","message":"cli-agents did not finish in time"}`
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The exe already wrote a JSON error payload on stdout.
			_ = exitErr
			return http.StatusBadRequest, string(out)
		}
		// gin marshals the message safely (Windows exec errors embed backslashes).
		payload, _ := json.Marshal(gin.H{"error": "exec_failed", "message": err.Error()})
		return http.StatusInternalServerError, string(payload)
	}
	return http.StatusOK, string(out)
}

func (h *Handler) GetCodingAgentsStatus(c *gin.Context) {
	code, out := h.runAgentsExe("--json")
	c.Data(code, "application/json", []byte(out))
}

func (h *Handler) GetCodingAgentsBackups(c *gin.Context) {
	code, out := h.runAgentsExe("--json", "--backups")
	c.Data(code, "application/json", []byte(out))
}

// GetCodingAgentsPlan returns the read-only "what will change" preview for one agent.
func (h *Handler) GetCodingAgentsPlan(c *gin.Context) {
	name := c.Query("name")
	if !agentNameRe.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name", "message": "name must match " + agentNameRe.String()})
		return
	}
	code, out := h.runAgentsExe("--json", "--plan", name)
	c.Data(code, "application/json", []byte(out))
}

func (h *Handler) PostCodingAgentsApply(c *gin.Context) {
	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Key  string `json:"key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || !agentNameRe.MatchString(body.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name", "message": "name must match " + agentNameRe.String()})
		return
	}
	args := []string{"--json", "--apply", body.Name, "--init"}
	// --flag=value form: a value starting with "-" can never be re-tokenized
	// as a flag by the exe's argv parser.
	if body.URL != "" {
		args = append(args, "--url="+body.URL)
	}
	if body.Key != "" {
		args = append(args, "--key="+body.Key)
	}
	code, out := h.runAgentsExe(args...)
	c.Data(code, "application/json", []byte(out))
}

func (h *Handler) PostCodingAgentsRevert(c *gin.Context) {
	var body struct {
		Stamp string `json:"stamp"`
	}
	// Empty body = revert latest; malformed JSON must NOT silently become the
	// destructive revert-latest default.
	if err := c.ShouldBindJSON(&body); err != nil && c.Request.ContentLength > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": err.Error()})
		return
	}
	args := []string{"--json", "--revert"}
	if body.Stamp != "" {
		if !agentStampRe.MatchString(body.Stamp) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_stamp"})
			return
		}
		args = append(args, body.Stamp)
	}
	code, out := h.runAgentsExe(args...)
	c.Data(code, "application/json", []byte(out))
}
