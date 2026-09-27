package server

import (
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

func TestTrafficAccumulatorAggregatesBySecondAndTunnel(t *testing.T) {
	acc := newTrafficAccumulator()
	base := time.Unix(1_700_000_000, 123_000_000).UTC()

	if err := acc.Add(base, "", "c1", "web", "http", 100, 10); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := acc.Add(base.Add(500*time.Millisecond), "", "c1", "web", "http", 50, 5); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := acc.Add(base.Add(time.Second), "", "c1", "web", "http", 7, 3); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := acc.Add(base, "", "c1", "ssh", "tcp", 20, 2); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if got := acc.Len(); got != 3 {
		t.Fatalf("pending buckets: want 3, got %d", got)
	}

	deltas := acc.Drain()
	if len(deltas) != 3 {
		t.Fatalf("drained deltas: want 3, got %+v", deltas)
	}
	if got := acc.Len(); got != 0 {
		t.Fatalf("pending buckets after drain: want 0, got %d", got)
	}

	first := deltas[0]
	if first.ClientID != "c1" || first.TunnelName != "ssh" || first.TunnelType != "tcp" {
		t.Fatalf("first sorted delta should be ssh/tcp, got %+v", first)
	}
	webFirst := deltas[1]
	if webFirst.TunnelName != "web" || webFirst.SecondStart != secondFloorUTC(base).Unix() {
		t.Fatalf("first web delta mismatch: %+v", webFirst)
	}
	if webFirst.IngressBytes != 150 || webFirst.EgressBytes != 15 {
		t.Fatalf("web same-second aggregation mismatch: %+v", webFirst)
	}
	webSecond := deltas[2]
	if webSecond.TunnelName != "web" || webSecond.SecondStart != secondFloorUTC(base.Add(time.Second)).Unix() {
		t.Fatalf("second web delta mismatch: %+v", webSecond)
	}
	if webSecond.IngressBytes != 7 || webSecond.EgressBytes != 3 {
		t.Fatalf("web next-second aggregation mismatch: %+v", webSecond)
	}
}

func TestTrafficAccumulatorKeepsTunnelRevisionsSeparate(t *testing.T) {
	acc := newTrafficAccumulator()
	now := time.Unix(1_700_000_000, 0).UTC()
	base := TrafficDelta{
		TunnelID:     "tunnel-1",
		ClientID:     "client-1",
		TunnelName:   "web",
		TunnelType:   "tcp",
		IngressBytes: 10,
		EgressBytes:  4,
	}
	oldRevision := base
	oldRevision.Revision = 7
	newRevision := base
	newRevision.Revision = 8
	newRevision.IngressBytes = 20
	newRevision.EgressBytes = 9
	if err := acc.AddDelta(now, oldRevision); err != nil {
		t.Fatalf("add old revision: %v", err)
	}
	if err := acc.AddDelta(now, newRevision); err != nil {
		t.Fatalf("add new revision: %v", err)
	}

	deltas := acc.Drain()
	if len(deltas) != 2 {
		t.Fatalf("different revisions must not merge, got %+v", deltas)
	}
	if deltas[0].Revision != 7 || deltas[0].IngressBytes != 10 || deltas[1].Revision != 8 || deltas[1].IngressBytes != 20 {
		t.Fatalf("revision-separated deltas mismatch: %+v", deltas)
	}
}

func TestTrafficAccumulatorKeepsRelayAndDirectTransportSeparate(t *testing.T) {
	acc := newTrafficAccumulator()
	now := time.Unix(1_700_000_000, 0).UTC()
	base := TrafficDelta{TunnelID: "t1", Revision: 1, ClientID: "owner", TunnelName: "tcp", TunnelType: "tcp", Transport: protocol.ActualTransportServerRelay, IngressBytes: 10}
	if err := acc.AddDelta(now, base); err != nil {
		t.Fatal(err)
	}
	direct := base
	direct.Transport = protocol.ActualTransportPeerDirect
	direct.IngressBytes = 20
	if err := acc.AddDelta(now, direct); err != nil {
		t.Fatal(err)
	}
	deltas := acc.Drain()
	if len(deltas) != 2 {
		t.Fatalf("transport buckets merged: %+v", deltas)
	}
	seen := map[string]uint64{}
	for _, delta := range deltas {
		seen[delta.Transport] = delta.IngressBytes
	}
	if seen[protocol.ActualTransportServerRelay] != 10 || seen[protocol.ActualTransportPeerDirect] != 20 {
		t.Fatalf("transport deltas=%+v", deltas)
	}
}

func TestRecordStoredTunnelTrafficKeepsOldRelayStreamInRelayBucketAfterSelectorTurnsDirect(t *testing.T) {
	fixture := newTrafficOwnerFixture(t)
	s := fixture.server
	stored := fixture.tunnelA
	stored.ActualTransport = protocol.ActualTransportPeerDirect
	s.recordStoredTunnelTrafficAt(time.Now(), stored, 17, 19)
	deltas := s.trafficAccumulator.Drain()
	if len(deltas) != 1 || deltas[0].Transport != protocol.ActualTransportServerRelay || deltas[0].IngressBytes != 17 || deltas[0].EgressBytes != 19 {
		t.Fatalf("old relay stream was attributed to selector transport: %+v", deltas)
	}
}

func TestTrafficAccumulatorResetTunnelClearsQueuedAndRejectsLateOldRevision(t *testing.T) {
	acc := newTrafficAccumulator()
	now := time.Unix(1_700_000_000, 0).UTC()
	old := TrafficDelta{
		TunnelID:     "tunnel-1",
		Revision:     7,
		ClientID:     "client-old",
		TunnelName:   "web",
		TunnelType:   "tcp",
		IngressBytes: 10,
		EgressBytes:  4,
	}
	unrelated := old
	unrelated.TunnelID = "tunnel-2"
	unrelated.ClientID = "client-other"
	unrelated.TunnelName = "other"
	if err := acc.AddDelta(now, old); err != nil {
		t.Fatalf("queue old revision: %v", err)
	}
	if err := acc.AddDelta(now, unrelated); err != nil {
		t.Fatalf("queue unrelated tunnel: %v", err)
	}

	acc.ResetTunnel(old.TunnelID, 8)
	if got := acc.Len(); got != 1 {
		t.Fatalf("reset should physically remove only migrated tunnel, pending = %d, want 1", got)
	}
	lateOld := old
	lateOld.IngressBytes = 100
	if err := acc.AddDelta(now, lateOld); err != nil {
		t.Fatalf("add late old revision: %v", err)
	}
	fresh := old
	fresh.Revision = 8
	fresh.ClientID = "client-new"
	fresh.IngressBytes = 7
	if err := acc.AddDelta(now, fresh); err != nil {
		t.Fatalf("add fresh revision: %v", err)
	}

	deltas := acc.Drain()
	if len(deltas) != 2 {
		t.Fatalf("drain should contain unrelated and fresh revisions only, got %+v", deltas)
	}
	for _, delta := range deltas {
		if delta.TunnelID == old.TunnelID && (delta.Revision != 8 || delta.IngressBytes != 7) {
			t.Fatalf("late old revision survived reset: %+v", deltas)
		}
	}
}

func TestTrafficDeltaFactoriesPropagateTunnelRevision(t *testing.T) {
	proxyDelta := trafficDeltaFromProxyConfig("client-1", protocol.ProxyConfig{
		ID:       "proxy-tunnel",
		Name:     "web",
		Type:     protocol.ProxyTypeTCP,
		Revision: 9,
	}, 1, 2)
	if proxyDelta.Revision != 9 {
		t.Fatalf("proxy-config traffic revision = %d, want 9", proxyDelta.Revision)
	}

	storedDelta := trafficDeltaFromStoredTunnel(StoredTunnel{
		ProxyNewRequest: protocol.ProxyNewRequest{ID: "stored-tunnel", Name: "relay", Type: protocol.ProxyTypeTCP},
		ClientID:        "client-2",
		Revision:        11,
	}, 3, 4)
	if storedDelta.Revision != 11 {
		t.Fatalf("stored-tunnel traffic revision = %d, want 11", storedDelta.Revision)
	}
}

func TestServerFlushTrafficObservationsAppliesAccumulator(t *testing.T) {
	fixture := newTrafficOwnerFixture(t)
	s := fixture.server
	ts := fixture.store

	base := secondFloorUTC(time.Now().UTC())
	s.recordTrafficObservationAt(base, fixture.tunnelA.ID, fixture.clientA.ID, fixture.tunnelA.Name, fixture.tunnelA.Type, 100, 10)
	s.recordTrafficObservationAt(base, fixture.tunnelA.ID, fixture.clientA.ID, fixture.tunnelA.Name, fixture.tunnelA.Type, 50, 5)

	before := mustQueryWithResolution(t, ts, fixture.clientA.ID, fixture.tunnelA.Name, base.Add(-time.Second), base.Add(time.Second), TrafficResolutionSecond)
	if len(before.Items) != 0 {
		t.Fatalf("traffic store should not see accumulator data before flush, got %+v", before.Items)
	}

	s.flushTrafficObservations()

	after := mustQueryWithResolution(t, ts, fixture.clientA.ID, fixture.tunnelA.Name, base.Add(-time.Second), base.Add(time.Second), TrafficResolutionSecond)
	web := mustSingleSeries(t, after, fixture.tunnelA.Name)
	if len(web.Points) != 1 {
		t.Fatalf("web points: want 1, got %+v", web.Points)
	}
	if web.Points[0].IngressBytes != 150 || web.Points[0].EgressBytes != 15 || web.Points[0].TotalBytes != 165 {
		t.Fatalf("flushed point mismatch: %+v", web.Points[0])
	}
}

func TestServerFlushTrafficObservationsDropsDeltaQueuedBeforeMigration(t *testing.T) {
	fixture := newTrafficOwnerFixture(t)
	store := fixture.server.store
	trafficStore := fixture.store
	s := fixture.server
	stored := fixture.tunnelA
	newClient, err := s.auth.adminStore.GetOrCreateClientForUser(fixture.ownerA.ID, "traffic-migration-target-install", protocol.ClientInfo{Hostname: "traffic-migration-target"}, "127.0.0.3")
	if err != nil {
		t.Fatalf("register migration target: %v", err)
	}
	now := secondFloorUTC(time.Now().UTC())

	s.recordStoredTunnelTrafficAt(now, stored, 100, 40)
	replacement := stored
	replacement.ClientID = newClient.ID
	replacement.OwnerClientID = newClient.ID
	replacement.Target.ClientID = newClient.ID
	replacement.UpdatedAt = time.Time{}
	_, migrated, err := store.MigrateTunnelTargetByID(stored.ID, stored.Revision, replacement)
	if err != nil {
		t.Fatalf("MigrateTunnelTargetByID failed: %v", err)
	}
	if got := s.trafficAccumulator.Len(); got != 0 {
		t.Fatalf("migration should physically clear queued accumulator traffic, pending = %d", got)
	}
	s.recordStoredTunnelTrafficAt(now, stored, 50, 20)
	if got := s.trafficAccumulator.Len(); got != 0 {
		t.Fatalf("late old-revision traffic should be rejected before queueing, pending = %d", got)
	}
	s.flushTrafficObservations()
	oldResult := mustQueryWithResolution(t, trafficStore, fixture.clientA.ID, stored.ID, now.Add(-time.Minute), now.Add(time.Minute), TrafficResolutionMinute)
	if len(oldResult.Items) != 0 {
		t.Fatalf("queued old-revision traffic should be filtered after migration, got %+v", oldResult.Items)
	}

	s.recordStoredTunnelTrafficAt(now, migrated, 7, 3)
	s.flushTrafficObservations()
	newResult := mustQueryWithResolution(t, trafficStore, newClient.ID, migrated.ID, now.Add(-time.Minute), now.Add(time.Minute), TrafficResolutionMinute)
	series := mustSingleSeries(t, newResult, migrated.Name)
	if len(series.Points) != 1 || series.Points[0].IngressBytes != 7 || series.Points[0].EgressBytes != 3 {
		t.Fatalf("new revision traffic mismatch: %+v", series.Points)
	}
}
