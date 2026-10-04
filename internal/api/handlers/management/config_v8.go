package management

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

// ConfigV8ContextKey marks operations registered by the v8 API that save configuration.
const ConfigV8ContextKey = "management.config-v8"

// ConfigV8 serves the persisted configuration using v8 names. GET is a view;
// only a successful configuration mutation migrates the stored document.
func (h *Handler) ConfigV8(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := os.ReadFile(h.configFilePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read_failed"})
		return
	}
	data, _, err = config.NormalizeConfigLayout(data, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(data, &doc); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config"})
		return
	}
	root := doc.Content[0]
	path := strings.Trim(c.Param("path"), "/")
	parts := []string{}
	if path != "" {
		parts = strings.Split(path, "/")
	}
	yamlRequest := strings.HasSuffix(c.FullPath(), "/config.yaml")
	if c.Request.Method == http.MethodGet {
		config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
		if !yamlRequest {
			if servers := configV8Node(root, v8ICEServersPath); servers != nil && servers.Kind == yaml.SequenceNode {
				for _, server := range servers.Content {
					deleteConfigV8Path(server, []string{"username"})
					deleteConfigV8Path(server, []string{"credential"})
				}
			}
			h.injectV8APIKeyAuthIndexesLocked(root, data)
		}
		value := configV8Node(root, parts)
		if value == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.Header("Cache-Control", "no-store")
		if yamlRequest {
			c.Data(http.StatusOK, "application/yaml; charset=utf-8", data)
			return
		}
		var result any
		if err = value.Decode(&result); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decode_failed"})
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	before := cloneConfigV8Node(root)
	config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
	if c.Request.Method == http.MethodDelete {
		if len(parts) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_delete_config"})
			return
		}
		if !deleteConfigV8Path(root, parts) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
	} else {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		if !yamlRequest && !json.Valid(body) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
			return
		}
		var update yaml.Node
		if err = yaml.Unmarshal(body, &update); err != nil || len(update.Content) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		stripAPIKeysAuthIndexesFromUpdate(parts, &update)
		if len(parts) == 0 && update.Content[0].Kind != yaml.MappingNode {
			c.JSON(http.StatusBadRequest, gin.H{"error": "config_must_be_object"})
			return
		}
		if len(parts) == 0 {
			if err = config.NormalizeV8ConfigAliases(update.Content[0]); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
				return
			}
		}
		// Paths identify YAML keys, never array indexes. Lists are replaced whole.
		dst := root
		for _, part := range parts {
			if part == "" || dst.Kind != yaml.MappingNode {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path"})
				return
			}
			next := configV8Node(dst, []string{part})
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				dst.Content = append(dst.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, next)
			}
			dst = next
		}
		if c.Request.Method == http.MethodPatch {
			mergeConfigV8Patch(dst, update.Content[0])
		} else {
			if dst.Kind == yaml.ScalarNode && update.Content[0].Kind == yaml.ScalarNode {
				update.Content[0].HeadComment = dst.HeadComment
				update.Content[0].LineComment = dst.LineComment
				update.Content[0].FootComment = dst.FootComment
			}
			*dst = *update.Content[0]
		}
	}
	if err = config.NormalizeV8ConfigAliases(root); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if !yamlRequest && c.Request.Method != http.MethodDelete {
		preserveV8TURNSecrets(root, before)
	}
	// Revisions are owned by Home and must not become ordinary editable settings.
	for _, field := range []string{"credentials/concurrency/lifecycle-config-revision", "credentials/concurrency/observation-barrier-revision", "plugins/auth-revision"} {
		old := configV8Node(before, strings.Split(field, "/"))
		next := configV8Node(root, strings.Split(field, "/"))
		var a, b any
		if old != nil {
			_ = old.Decode(&a)
		}
		if next != nil {
			_ = next.Decode(&b)
		}
		if !reflect.DeepEqual(a, b) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "read_only_field", "field": field})
			return
		}
	}
	stripAPIKeysAuthIndexesFromRoot(root)
	data, err = yaml.Marshal(&doc)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	next, err := config.ParseConfigBytes(data)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if err = config.ValidateV8Config(data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	data, _, err = config.NormalizeConfigLayout(data, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	// Save the validated canonical tree directly: projecting runtime defaults
	// back onto it loses explicit nulls, empty maps, and opaque plugin settings.
	// WriteConfig retains the inode of a mounted configuration file.
	if err = WriteConfig(h.configFilePath, data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": err.Error()})
		return
	}
	// Runtime-only ownership state is never sourced from an uploaded YAML document.
	next.Home = h.cfg.Home
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "config-version": 8})
}

var v8ICEServersPath = []string{"oauth", "providers", "codex", "live-media-relay", "ice-servers"}

// JSON reads redact TURN secrets. Preserve omitted credentials when a client
// writes that JSON back, matching by endpoint rather than array position so a
// changed URL cannot inherit credentials for a different server. Explicit empty
// strings/null clear a secret; YAML writes retain full replacement semantics.
func preserveV8TURNSecrets(root, before *yaml.Node) {
	next := configV8Node(root, v8ICEServersPath)
	previous := configV8Node(before, v8ICEServersPath)
	if next == nil || previous == nil || next.Kind != yaml.SequenceNode || previous.Kind != yaml.SequenceNode {
		return
	}
	matched := make([]bool, len(previous.Content))
	for _, server := range next.Content {
		urls := configV8Node(server, []string{"urls"})
		if urls == nil {
			continue
		}
		var newURLs []string
		if urls.Decode(&newURLs) != nil {
			continue
		}
		for i, old := range previous.Content {
			if matched[i] {
				continue
			}
			oldURLs := configV8Node(old, []string{"urls"})
			var previousURLs []string
			if oldURLs == nil || oldURLs.Decode(&previousURLs) != nil || !reflect.DeepEqual(newURLs, previousURLs) {
				continue
			}
			matched[i] = true
			for _, name := range []string{"username", "credential"} {
				if configV8Node(server, []string{name}) == nil {
					if secret := configV8Node(old, []string{name}); secret != nil {
						server.Content = append(server.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, cloneConfigV8Node(secret))
					}
				}
			}
			break
		}
	}
}

func configV8Node(root *yaml.Node, parts []string) *yaml.Node {
	for _, part := range parts {
		if root == nil || root.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == part {
				next = root.Content[i+1]
				break
			}
		}
		root = next
	}
	return root
}

// deleteConfigV8Path prunes only empty ancestors of the removed field. Other
// explicit empty maps can carry inheritance or plugin semantics and stay intact.
func deleteConfigV8Path(root *yaml.Node, parts []string) bool {
	if root == nil || root.Kind != yaml.MappingNode || len(parts) == 0 {
		return false
	}
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != parts[0] {
			continue
		}
		if len(parts) > 1 {
			child := root.Content[i+1]
			if !deleteConfigV8Path(child, parts[1:]) {
				return false
			}
			if len(child.Content) > 0 {
				return true
			}
		}
		root.Content = append(root.Content[:i], root.Content[i+2:]...)
		return true
	}
	return false
}

func cloneConfigV8Node(node *yaml.Node) *yaml.Node {
	copy := *node
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = cloneConfigV8Node(child)
	}
	return &copy
}

// Unlike JSON merge-patch, null is retained: optional key overrides use it to
// inherit the group value. DELETE is the explicit field-removal operation.
func mergeConfigV8Patch(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		if dst.Kind == yaml.ScalarNode && src.Kind == yaml.ScalarNode {
			copy := *src
			copy.HeadComment, copy.LineComment, copy.FootComment = dst.HeadComment, dst.LineComment, dst.FootComment
			*dst = copy
			return
		}
		*dst = *src
		return
	}
	for i := 0; i < len(src.Content); i += 2 {
		key, value := src.Content[i], src.Content[i+1]
		if old := configV8Node(dst, []string{key.Value}); old != nil {
			mergeConfigV8Patch(old, value)
		} else {
			dst.Content = append(dst.Content, key, value)
		}
	}
}
