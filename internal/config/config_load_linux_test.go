package config

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sys/unix"
)

func TestLoadConfigForSaveRejectsHashWriteFailure(t *testing.T) {
	for _, section := range []string{"remote-management", "management"} {
		t.Run(section, func(t *testing.T) {
			const fixture = "staged-write-test-only"
			raw := "port: 8317\n" + section + ": {secret-key: " + fixture + "}\n"
			// A sealed memfd fails writes even as root, without permission checks,
			// races, process-wide limits, or replacing the filesystem implementation.
			fd, err := unix.MemfdCreate("config-write-test", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
			if err != nil {
				t.Fatal(err)
			}
			file := os.NewFile(uintptr(fd), "config-write-test")
			t.Cleanup(func() {
				if err := file.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := file.WriteString(raw); err != nil {
				t.Fatal(err)
			}
			if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_WRITE|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW); err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/proc/self/fd/%d", file.Fd())
			cfg, err := LoadConfigForSave(path)
			if !errors.Is(err, unix.EPERM) || cfg != nil {
				t.Errorf("staged load returned config=%t, error=%v; want no config and the write failure", cfg != nil, err)
			}
			// Startup remains best effort: a read-only config can run with its
			// in-memory hash even when it cannot store that hash for the next load.
			for _, optional := range []bool{false, true} {
				loaded, err := LoadConfigOptional(path, optional)
				if err != nil {
					t.Fatal(err)
				}
				if err := bcrypt.CompareHashAndPassword([]byte(loaded.RemoteManagement.SecretKey), []byte(fixture)); err != nil {
					t.Fatal("startup load lost its in-memory management-key hash")
				}
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != raw {
				t.Fatal("failed hash write changed the source file")
			}
		})
	}
}
