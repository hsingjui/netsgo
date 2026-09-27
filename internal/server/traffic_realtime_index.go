package server

type realtimeSecondIndex struct {
	byClient map[string]map[realtimeSecondSeriesKey]map[int64]TrafficBucket
}

// realtimeSecondSeriesKey keeps observations from distinct user owners apart
// even before they reach SQLite. A ClientID is immutable to one owner in the
// normal model, but retaining the owner here makes a bad or stale producer
// unable to merge data across an authorization boundary.
type realtimeSecondSeriesKey struct {
	OwnerUserID string
	Series      trafficSeriesKey
}

func newRealtimeSecondIndex() *realtimeSecondIndex {
	return &realtimeSecondIndex{byClient: make(map[string]map[realtimeSecondSeriesKey]map[int64]TrafficBucket)}
}

func (idx *realtimeSecondIndex) Add(bucket TrafficBucket) error {
	if idx == nil || bucket.ClientID == "" || bucket.TunnelName == "" || bucket.TunnelType == "" {
		return nil
	}
	if bucket.IngressBytes == 0 && bucket.EgressBytes == 0 {
		return nil
	}
	if idx.byClient == nil {
		idx.byClient = make(map[string]map[realtimeSecondSeriesKey]map[int64]TrafficBucket)
	}

	seriesByClient := idx.byClient[bucket.ClientID]
	if seriesByClient == nil {
		seriesByClient = make(map[realtimeSecondSeriesKey]map[int64]TrafficBucket)
		idx.byClient[bucket.ClientID] = seriesByClient
	}

	seriesKey := realtimeSecondSeriesKey{OwnerUserID: bucket.OwnerUserID, Series: trafficSeriesKeyFromBucket(bucket)}
	bucketsBySecond := seriesByClient[seriesKey]
	if bucketsBySecond == nil {
		bucketsBySecond = make(map[int64]TrafficBucket)
		seriesByClient[seriesKey] = bucketsBySecond
	}

	if existing, ok := bucketsBySecond[bucket.BucketStart]; ok {
		if err := addTrafficBucketValues(&existing, bucket); err != nil {
			return err
		}
		bucketsBySecond[bucket.BucketStart] = existing
		return nil
	}

	bucketsBySecond[bucket.BucketStart] = bucket
	return nil
}

func (idx *realtimeSecondIndex) Query(clientID, tunnelName string, fromUnix, toUnix int64) []TrafficBucket {
	return idx.query("", clientID, tunnelName, fromUnix, toUnix, false)
}

func (idx *realtimeSecondIndex) QueryForUser(ownerUserID, clientID, tunnelName string, fromUnix, toUnix int64) []TrafficBucket {
	if ownerUserID == "" {
		return nil
	}
	return idx.query(ownerUserID, clientID, tunnelName, fromUnix, toUnix, true)
}

func (idx *realtimeSecondIndex) query(ownerUserID, clientID, tunnelName string, fromUnix, toUnix int64, requireOwner bool) []TrafficBucket {
	if idx == nil || idx.byClient == nil {
		return nil
	}

	seriesByClient := idx.byClient[clientID]
	if len(seriesByClient) == 0 {
		return nil
	}

	buckets := []TrafficBucket{}
	for key, bucketsBySecond := range seriesByClient {
		if requireOwner && key.OwnerUserID != ownerUserID {
			continue
		}
		if tunnelName != "" && key.Series.TunnelName != tunnelName && key.Series.TunnelID != tunnelName {
			continue
		}
		for second, bucket := range bucketsBySecond {
			if second >= fromUnix && second <= toUnix {
				buckets = append(buckets, bucket)
			}
		}
	}
	return buckets
}

func (idx *realtimeSecondIndex) PruneBefore(cutoff int64) {
	if idx == nil || idx.byClient == nil {
		return
	}

	for clientID, seriesByClient := range idx.byClient {
		for key, bucketsBySecond := range seriesByClient {
			for second := range bucketsBySecond {
				if second < cutoff {
					delete(bucketsBySecond, second)
				}
			}
			if len(bucketsBySecond) == 0 {
				delete(seriesByClient, key)
			}
		}
		if len(seriesByClient) == 0 {
			delete(idx.byClient, clientID)
		}
	}
}

func (idx *realtimeSecondIndex) EvictClient(clientID string) {
	if idx == nil || idx.byClient == nil {
		return
	}
	delete(idx.byClient, clientID)
}

func (idx *realtimeSecondIndex) EvictTunnel(clientID, tunnelName string) {
	if idx == nil || idx.byClient == nil {
		return
	}
	seriesByClient := idx.byClient[clientID]
	for key := range seriesByClient {
		if key.Series.TunnelName == tunnelName {
			delete(seriesByClient, key)
		}
	}
	if len(seriesByClient) == 0 {
		delete(idx.byClient, clientID)
	}
}

func (idx *realtimeSecondIndex) RenameTunnel(clientID, oldName, newName string) error {
	renamed, changed, err := idx.renamedTunnelBuckets(clientID, oldName, newName)
	if err != nil || !changed {
		return err
	}
	idx.byClient[clientID] = renamed
	return nil
}

func (idx *realtimeSecondIndex) renamedTunnelBuckets(clientID, oldName, newName string) (map[realtimeSecondSeriesKey]map[int64]TrafficBucket, bool, error) {
	if idx == nil || idx.byClient == nil || oldName == newName {
		return nil, false, nil
	}
	seriesByClient := idx.byClient[clientID]
	if len(seriesByClient) == 0 {
		return nil, false, nil
	}

	hasOldName := false
	for key := range seriesByClient {
		if key.Series.TunnelName == oldName {
			hasOldName = true
			break
		}
	}
	if !hasOldName {
		return nil, false, nil
	}

	renamed := make(map[realtimeSecondSeriesKey]map[int64]TrafficBucket, len(seriesByClient))
	for key, bucketsBySecond := range seriesByClient {
		targetKey := key
		if key.Series.TunnelName == oldName {
			targetKey.Series.TunnelName = newName
		}

		targetBuckets := renamed[targetKey]
		if targetBuckets == nil {
			targetBuckets = make(map[int64]TrafficBucket, len(bucketsBySecond))
			renamed[targetKey] = targetBuckets
		}
		for second, bucket := range bucketsBySecond {
			if key.Series.TunnelName == oldName {
				bucket.TunnelName = newName
			}
			if existing, ok := targetBuckets[second]; ok {
				if err := addTrafficBucketValues(&existing, bucket); err != nil {
					return nil, false, err
				}
				targetBuckets[second] = existing
				continue
			}
			targetBuckets[second] = bucket
		}
	}
	return renamed, true, nil
}
