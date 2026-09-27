package server

import (
	"errors"
	"testing"
	"time"
)

func disableTrafficFixtureUser(t *testing.T, fixture trafficOwnerFixture) {
	t.Helper()
	if _, changed, err := fixture.server.auth.adminStore.SetUserStatus(fixture.ownerA.ID, fixture.ownerB.ID, UserStatusDisabled); err != nil {
		t.Fatalf("disable traffic fixture user: %v", err)
	} else if !changed {
		t.Fatal("traffic fixture user was already disabled")
	}
}

func trafficStoreMemoryCountForOwner(store *TrafficStore, ownerUserID string) (pendingMinute, realtimeSecond int) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, bucket := range store.pendingMinute {
		if bucket.OwnerUserID == ownerUserID {
			pendingMinute++
		}
	}
	if store.realtimeSecond == nil {
		return pendingMinute, 0
	}
	for _, seriesByClient := range store.realtimeSecond.byClient {
		for key, bucketsBySecond := range seriesByClient {
			if key.OwnerUserID == ownerUserID {
				realtimeSecond += len(bucketsBySecond)
			}
		}
	}
	return pendingMinute, realtimeSecond
}

func TestUserDeletionBoundaryEvictsOnlyDeletedOwnerTraffic(t *testing.T) {
	fixture := newTrafficOwnerFixture(t)
	disableTrafficFixtureUser(t, fixture)

	now := secondFloorUTC(time.Now().UTC())
	targetDelta := trafficOwnerDelta(fixture.tunnelB, now, 17, 7, true)
	otherDelta := trafficOwnerDelta(fixture.tunnelA, now, 31, 13, true)

	// Model a flush that drained immediately before the hard-delete acquired
	// TrafficStore.mu. Its local batch must be rejected after the commit.
	if err := fixture.server.trafficAccumulator.AddDelta(now, targetDelta); err != nil {
		t.Fatalf("queue target in-flight traffic: %v", err)
	}
	if err := fixture.server.trafficAccumulator.AddDelta(now, otherDelta); err != nil {
		t.Fatalf("queue other in-flight traffic: %v", err)
	}
	inFlight := fixture.server.trafficAccumulator.Drain()

	// Queue a second batch in every in-memory layer. Successful deletion must
	// physically evict the target while retaining the other user's traffic.
	if err := fixture.server.trafficAccumulator.AddDelta(now, targetDelta); err != nil {
		t.Fatalf("queue target accumulator traffic: %v", err)
	}
	if err := fixture.server.trafficAccumulator.AddDelta(now, otherDelta); err != nil {
		t.Fatalf("queue other accumulator traffic: %v", err)
	}
	fixture.store.ApplyDeltas([]TrafficDelta{targetDelta, otherDelta})

	err := fixture.store.withUserDeletionBoundary(fixture.ownerB.ID, func() error {
		return fixture.server.auth.adminStore.DeleteDisabledUser(fixture.ownerA.ID, fixture.ownerB.ID)
	})
	if err != nil {
		t.Fatalf("delete user inside traffic boundary: %v", err)
	}

	queuedAfterDelete := fixture.server.trafficAccumulator.Drain()
	if len(queuedAfterDelete) != 1 || queuedAfterDelete[0].OwnerUserID != fixture.ownerA.ID {
		t.Fatalf("accumulator after deletion = %+v, want only preserved owner %q", queuedAfterDelete, fixture.ownerA.ID)
	}
	if pending, realtime := trafficStoreMemoryCountForOwner(fixture.store, fixture.ownerB.ID); pending != 0 || realtime != 0 {
		t.Fatalf("deleted owner memory traffic = (%d pending, %d realtime), want zero", pending, realtime)
	}
	if pending, realtime := trafficStoreMemoryCountForOwner(fixture.store, fixture.ownerA.ID); pending == 0 || realtime == 0 {
		t.Fatalf("preserved owner memory traffic = (%d pending, %d realtime), want both non-zero", pending, realtime)
	}

	// Both an already-drained batch and observations arriving after commit are
	// ignored for the deleted immutable owner ID.
	fixture.store.ApplyDeltas(append(inFlight, queuedAfterDelete...))
	if err := fixture.server.trafficAccumulator.AddDelta(now.Add(time.Second), targetDelta); err != nil {
		t.Fatalf("late deleted-owner accumulator observation: %v", err)
	}
	fixture.store.ApplyDeltas([]TrafficDelta{targetDelta})
	if got := fixture.server.trafficAccumulator.Len(); got != 0 {
		t.Fatalf("late deleted-owner accumulator entries = %d, want 0", got)
	}
	if err := fixture.store.Flush(); err != nil {
		t.Fatalf("flush preserved owner after user deletion: %v", err)
	}

	var targetRows int
	if err := fixture.store.db.QueryRow(`SELECT COUNT(*) FROM traffic_buckets WHERE owner_user_id = ?`, fixture.ownerB.ID).Scan(&targetRows); err != nil {
		t.Fatalf("count deleted-owner traffic rows: %v", err)
	}
	if targetRows != 0 {
		t.Fatalf("deleted-owner persisted traffic rows = %d, want 0", targetRows)
	}
	result, err := fixture.store.QueryWithResolutionForUser(fixture.ownerA.ID, fixture.clientA.ID, fixture.tunnelA.ID, now.Add(-time.Minute), now.Add(time.Minute), TrafficResolutionMinute)
	if err != nil {
		t.Fatalf("query preserved owner traffic: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Points) != 1 || result.Items[0].Points[0].IngressBytes == 0 {
		t.Fatalf("preserved owner traffic after flush = %+v, want one non-empty point", result)
	}
}

func TestUserDeletionBoundaryFailureKeepsAllTrafficQueues(t *testing.T) {
	fixture := newTrafficOwnerFixture(t)
	disableTrafficFixtureUser(t, fixture)

	now := secondFloorUTC(time.Now().UTC())
	targetDelta := trafficOwnerDelta(fixture.tunnelB, now, 19, 5, true)
	otherDelta := trafficOwnerDelta(fixture.tunnelA, now, 37, 11, true)
	if err := fixture.server.trafficAccumulator.AddDelta(now, targetDelta); err != nil {
		t.Fatalf("queue target accumulator traffic: %v", err)
	}
	if err := fixture.server.trafficAccumulator.AddDelta(now, otherDelta); err != nil {
		t.Fatalf("queue other accumulator traffic: %v", err)
	}
	fixture.store.ApplyDeltas([]TrafficDelta{targetDelta, otherDelta})

	injected := errors.New("injected user deletion failure")
	fixture.server.auth.adminStore.failSaveErr = injected
	fixture.server.auth.adminStore.failSaveCount = 1
	err := fixture.store.withUserDeletionBoundary(fixture.ownerB.ID, func() error {
		return fixture.server.auth.adminStore.DeleteDisabledUser(fixture.ownerA.ID, fixture.ownerB.ID)
	})
	if !errors.Is(err, injected) {
		t.Fatalf("delete user inside traffic boundary error = %v, want injected failure", err)
	}

	if pending, realtime := trafficStoreMemoryCountForOwner(fixture.store, fixture.ownerB.ID); pending == 0 || realtime == 0 {
		t.Fatalf("failed deletion lost target memory traffic: %d pending, %d realtime", pending, realtime)
	}
	if pending, realtime := trafficStoreMemoryCountForOwner(fixture.store, fixture.ownerA.ID); pending == 0 || realtime == 0 {
		t.Fatalf("failed deletion lost other memory traffic: %d pending, %d realtime", pending, realtime)
	}
	drained := fixture.server.trafficAccumulator.Drain()
	ownerCounts := map[string]int{}
	for _, delta := range drained {
		ownerCounts[delta.OwnerUserID]++
	}
	if ownerCounts[fixture.ownerA.ID] != 1 || ownerCounts[fixture.ownerB.ID] != 1 {
		t.Fatalf("failed deletion accumulator owners = %+v, want one bucket for each owner", ownerCounts)
	}

	fixture.store.ApplyDeltas(drained)
	if err := fixture.store.Flush(); err != nil {
		t.Fatalf("flush after rolled-back user deletion: %v", err)
	}
	for _, check := range []struct {
		owner  User
		client RegisteredClient
		tunnel StoredTunnel
	}{
		{owner: fixture.ownerA, client: fixture.clientA, tunnel: fixture.tunnelA},
		{owner: fixture.ownerB, client: fixture.clientB, tunnel: fixture.tunnelB},
	} {
		result, err := fixture.store.QueryWithResolutionForUser(check.owner.ID, check.client.ID, check.tunnel.ID, now.Add(-time.Minute), now.Add(time.Minute), TrafficResolutionMinute)
		if err != nil {
			t.Fatalf("query owner %q after rolled-back deletion: %v", check.owner.ID, err)
		}
		if len(result.Items) != 1 || len(result.Items[0].Points) != 1 || result.Items[0].Points[0].IngressBytes == 0 {
			t.Fatalf("owner %q traffic after rolled-back deletion = %+v, want one non-empty point", check.owner.ID, result)
		}
	}
}
