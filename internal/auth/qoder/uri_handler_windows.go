//go:build windows

package qoder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

const vbsFileName = "qoder_login_handler.vbs"

// RegisterURIHandler registers the qoder:// URI protocol handler on Windows.
// It creates a VBS script that forwards the qoder:// callback URL to the local
// HTTP callback server, then registers the protocol in the Windows registry.
// Returns a cleanup function that should be deferred.
func RegisterURIHandler(callbackPort int) func() {
	vbsPath := filepath.Join(os.TempDir(), vbsFileName)

	vbsContent := fmt.Sprintf(`Set objHTTP = CreateObject("MSXML2.XMLHTTP")
On Error Resume Next
url = "http://127.0.0.1:%d/forward?url="
url = url & UrlEncode(WScript.Arguments(0))
objHTTP.Open "GET", url, False
objHTTP.send

Function UrlEncode(str)
  Dim result, i, c
  result = ""
  For i = 1 To Len(str)
    c = Mid(str, i, 1)
    If c = " " Then
      result = result & "+"
    ElseIf InStr("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~", c) > 0 Then
      result = result & c
    Else
      result = result & "%%" & Right("0" & Hex(Asc(c)), 2)
    End If
  Next
  UrlEncode = result
End Function
`, callbackPort)

	if err := os.WriteFile(vbsPath, []byte(vbsContent), 0o644); err != nil {
		log.Errorf("qoder: failed to write VBS handler script: %v", err)
		return func() {}
	}

	regCmds := [][]string{
		{"reg", "add", `HKCU\Software\Classes\qoder`, "/ve", "/t", "REG_SZ", "/d", "URL:QoderLogin", "/f"},
		{"reg", "add", `HKCU\Software\Classes\qoder`, "/v", "URL Protocol", "/t", "REG_SZ", "/d", "", "/f"},
		{"reg", "add", `HKCU\Software\Classes\qoder\shell`, "/f"},
		{"reg", "add", `HKCU\Software\Classes\qoder\shell\open`, "/f"},
		{"reg", "add", `HKCU\Software\Classes\qoder\shell\open\command`,
			"/ve", "/t", "REG_SZ", "/d", fmt.Sprintf(`wscript.exe "%s" %%1`, vbsPath), "/f"},
	}

	for _, args := range regCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = nil
		cmd.Stderr = nil
		_ = cmd.Run()
	}

	log.Infof("qoder: registered qoder:// URI handler (VBS: %s)", vbsPath)

	return func() {
		UnregisterURIHandler()
	}
}

// UnregisterURIHandler removes the qoder:// URI protocol handler from Windows registry
// and cleans up the temporary VBS script.
func UnregisterURIHandler() {
	cmd := exec.Command("reg", "delete", `HKCU\Software\Classes\qoder`, "/f")
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()

	vbsPath := filepath.Join(os.TempDir(), vbsFileName)
	if _, err := os.Stat(vbsPath); err == nil {
		_ = os.Remove(vbsPath)
	}

	log.Info("qoder: unregistered qoder:// URI handler")
}
