package antigravity

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestManagedSessionStoreStopBeforeStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewManagedSessionStore()
		defer store.Stop()
		if store.started || store.closed {
			t.Fatal("managed constructor started or closed the store")
		}
		select {
		case <-store.stopCh:
			t.Fatal("managed constructor closed the stop channel")
		default:
		}

		done := make(chan struct{})
		go func() {
			store.Stop()
			store.Stop()
			close(done)
		}()
		<-done
		store.Start()
		store.Start()
		if store.started || !store.closed {
			t.Fatal("Start after Stop must not launch cleanup")
		}
		select {
		case <-store.stopCh:
		default:
			t.Fatal("Stop did not close the stop channel")
		}
	})
}

func TestManagedSessionStoreRepeatedStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewManagedSessionStore()
		defer store.Stop()
		var starts sync.WaitGroup
		for range 32 {
			starts.Add(1)
			go func() {
				defer starts.Done()
				store.Start()
			}()
		}
		starts.Wait()
		if !store.started || store.closed {
			t.Fatal("Start did not activate the managed store")
		}

		// One stop signal must release the only cleanup worker, even after
		// concurrent repeated Start calls. Stop closes the channel on return.
		done := make(chan struct{})
		go func() {
			store.cleanupWG.Wait()
			close(done)
		}()
		store.stopCh <- struct{}{}
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("repeated Start launched more than one cleanup worker")
		}
	})
}

func TestManagedSessionStoreStopWaitsForCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewManagedSessionStore()
		store.Start()

		// Keep one registered cleanup task behind a channel barrier so both
		// the first Stop and a concurrent repeated Stop must wait for it.
		store.lifecycleMu.Lock()
		store.cleanupWG.Add(1)
		store.lifecycleMu.Unlock()
		releaseCleanup := make(chan struct{})
		go func() {
			<-releaseCleanup
			store.cleanupWG.Done()
		}()

		firstDone := make(chan struct{})
		go func() {
			store.Stop()
			close(firstDone)
		}()
		<-store.stopCh
		secondDone := make(chan struct{})
		go func() {
			store.Stop()
			close(secondDone)
		}()
		synctest.Wait()
		for _, done := range []<-chan struct{}{firstDone, secondDone} {
			select {
			case <-done:
				t.Error("Stop returned before all cleanup tasks exited")
			default:
			}
		}

		close(releaseCleanup)
		<-firstDone
		<-secondDone
		store.Stop()
	})
}

func TestManagedSessionStoreConcurrentStartStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for range 16 {
			store := NewManagedSessionStore()
			gate := make(chan struct{})
			var calls sync.WaitGroup
			for range 16 {
				calls.Add(2)
				go func() {
					defer calls.Done()
					<-gate
					store.Start()
				}()
				go func() {
					defer calls.Done()
					<-gate
					store.Stop()
				}()
			}
			close(gate)
			calls.Wait()
			store.Stop()
			store.Start()
			if !store.closed {
				t.Fatal("concurrent Start/Stop did not leave the store closed")
			}
		}
	})
}

func TestNewSessionStoreAutoStartsCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewSessionStore()
		defer store.Stop()
		if !store.started || store.closed {
			t.Fatal("legacy constructor must start cleanup")
		}
		done := make(chan struct{})
		go func() {
			store.cleanupWG.Wait()
			close(done)
		}()
		store.stopCh <- struct{}{}
		<-done
	})
}

func TestManagedSessionStorePreservesSessionAccess(t *testing.T) {
	store := NewManagedSessionStore()
	defer store.Stop()
	live := &OAuthSession{State: "live", CreatedAt: time.Now()}
	expired := &OAuthSession{State: "expired", CreatedAt: time.Now().Add(-SessionTTL - time.Second)}
	store.Set("live", live)
	store.Set("expired", expired)
	if got, ok := store.Get("live"); !ok || got != live {
		t.Fatal("managed store must support session access before Start")
	}
	if _, ok := store.Get("expired"); ok {
		t.Fatal("expired session was returned")
	}
	store.Delete("expired")
	if _, ok := store.Get("expired"); ok {
		t.Fatal("deleted session was returned")
	}

	store.Start()
	store.Stop()
	if got, ok := store.Get("live"); !ok || got != live {
		t.Fatal("lifecycle changed stored sessions")
	}
	store.Delete("live")
	if _, ok := store.Get("live"); ok {
		t.Fatal("Delete stopped working after Stop")
	}
}
