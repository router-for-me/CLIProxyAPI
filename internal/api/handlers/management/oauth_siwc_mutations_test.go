package management

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestSIWCFileMutationRejectsLateExchange(t *testing.T) {
	for _, operation := range []string{"upload", "delete", "delete-all", "delete-batch"} {
		t.Run(operation, func(t *testing.T) {
			f := setupSIWCFlow(t, true)
			f.router.POST("/credentials", f.h.UploadAuthFileV8)
			f.router.DELETE("/credentials", f.h.DeleteAuthFileV8)
			state, flow := f.start(t)
			w := siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"state": state, "redirect_url": siwcCallbackURL(state), "provider": "codex"})
			if w.Code != http.StatusOK {
				t.Fatalf("callback: %d %s", w.Code, w.Body.String())
			}
			<-f.service.inputs
			original, errRead := os.ReadFile(f.path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			switch operation {
			case "upload":
				w = siwcRequest(t, f.router, http.MethodPost, "/credentials?name=codex.json", json.RawMessage(original))
			case "delete":
				w = siwcRequest(t, f.router, http.MethodDelete, "/credentials?name=codex.json", nil)
			case "delete-all":
				if errWrite := f.h.writeAuthFile(context.Background(), "other.json", original); errWrite != nil {
					t.Fatal(errWrite)
				}
				w = siwcRequest(t, f.router, http.MethodDelete, "/credentials?all=true", nil)
			case "delete-batch":
				if errWrite := f.h.writeAuthFile(context.Background(), "other.json", original); errWrite != nil {
					t.Fatal(errWrite)
				}
				w = siwcRequest(t, f.router, http.MethodDelete, "/credentials", []string{"codex.json", "other.json"})
			}
			if w.Code != http.StatusOK {
				t.Fatalf("mutation: %d %s", w.Code, w.Body.String())
			}
			session, ok := f.store.Get(state)
			if !ok || session.Status == "" || session.Completed || flow.ctx.Err() == nil {
				t.Fatal("mutation did not invalidate pending binding")
			}
			f.unblock()
			waitSIWCExchange(t, flow)
			if operation == "upload" {
				data, err := os.ReadFile(f.path)
				if err != nil || string(data) != string(original) {
					t.Fatalf("late exchange changed reupload: %s, %v", data, err)
				}
			} else {
				if _, err := os.Stat(f.path); !os.IsNotExist(err) {
					t.Fatalf("deleted file resurrected: %v", err)
				}
				if _, ok := f.manager.GetByID(f.auth.ID); ok {
					t.Fatal("deleted registration resurrected")
				}
			}
		})
	}
}

func TestSIWCFileMutationScopeAndValidation(t *testing.T) {
	f := setupSIWCFlow(t, false)
	f.router.POST("/credentials", f.h.UploadAuthFileV8)
	f.router.POST("/legacy", f.h.UploadAuthFile)
	state, flow := f.start(t)
	for _, test := range []struct {
		path   string
		body   any
		status int
	}{
		{"/credentials?name=codex.json", map[string]any{"excluded_models": 123}, http.StatusBadRequest},
		{"/credentials?name=other.json", f.auth.Metadata, http.StatusOK},
		{"/legacy?name=codex.json", f.auth.Metadata, http.StatusOK},
	} {
		w := siwcRequest(t, f.router, http.MethodPost, test.path, test.body)
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, w.Code, w.Body.String())
		}
		if flow.ctx.Err() != nil || !f.store.IsPending(state, "codex") {
			t.Fatalf("%s invalidated unrelated or legacy binding", test.path)
		}
	}
	f.h.postAuthPersistHook = func(_ context.Context, _ *coreauth.Auth) error {
		if f.h.siwcCommitMu.TryLock() {
			f.h.siwcCommitMu.Unlock()
			t.Error("commit gate released before publication hook")
		}
		return nil
	}
	w := siwcRequest(t, f.router, http.MethodPost, "/credentials?name=codex.json", f.auth.Metadata)
	if w.Code != http.StatusOK || flow.ctx.Err() == nil {
		t.Fatalf("reupload failed to invalidate binding: %d", w.Code)
	}
}

func TestSIWCMutationPathAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.json")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !sameSIWCMutationPath(alias, path) || sameSIWCMutationPath(path, filepath.Join(dir, "other.json")) {
		t.Fatal("incorrect canonical path matching")
	}
}
