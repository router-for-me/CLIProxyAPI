package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// Run with -race to check that scheduler snapshots never read published auths unlocked.
func TestPublishedAuthSnapshotRace(t *testing.T) {
	for _, operation := range []string{"Register", "Update", "UpdatePreparedAuth", "UpdateRefreshedAuth"} {
		t.Run(operation, func(t *testing.T) {
			ctx := WithSkipPersist(context.Background())
			m := NewManager(nil, nil, nil)
			a := &Auth{
				ID: "snapshot-race", Provider: "audit", Status: StatusActive,
				Metadata: map[string]any{"type": "audit"},
			}
			for i := 0; i < 200; i++ {
				a.Metadata[fmt.Sprintf("field-%d", i)] = i
			}
			if _, errRegister := m.Register(ctx, a); errRegister != nil {
				t.Fatal(errRegister)
			}

			start := make(chan struct{})
			var wg sync.WaitGroup
			for worker := 0; worker < 32; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					for i := 0; i < 100; i++ {
						m.MarkResult(ctx, Result{
							AuthID: a.ID, Provider: a.Provider,
							Model: fmt.Sprintf("model-%d", i%8), Success: true,
						})
					}
				}()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for i := 0; i < 100; i++ {
					base, ok := m.GetByID(a.ID)
					if !ok {
						t.Error("missing auth")
						return
					}
					updated := base.Clone()
					updated.Metadata["note"] = fmt.Sprint(i)
					var errPublish error
					switch operation {
					case "Register":
						_, errPublish = m.Register(ctx, updated)
					case "Update":
						_, errPublish = m.Update(ctx, updated)
					case "UpdatePreparedAuth":
						_, errPublish = m.UpdatePreparedAuth(ctx, base, updated)
					case "UpdateRefreshedAuth":
						_, errPublish = m.UpdateRefreshedAuth(ctx, base, updated)
					}
					if errPublish != nil {
						t.Error(errPublish)
						return
					}
				}
			}()
			close(start)
			wg.Wait()
		})
	}
}
