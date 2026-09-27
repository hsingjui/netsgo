package server

import (
	"database/sql"
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// ResolveClientTokenOwner resolves only the lifecycle gate that must protect a
// later token validation. It deliberately does not refresh token activity or
// otherwise mutate authentication state.
func (s *AdminStore) ResolveClientTokenOwner(rawToken string) (string, error) {
	if rawToken == "" {
		return "", ErrClientTokenInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var ownerUserID sql.NullString
	err := s.db.QueryRow(`SELECT rc.owner_user_id
		FROM client_tokens AS ct
		LEFT JOIN registered_clients AS rc ON rc.id = ct.client_id
		WHERE ct.token_hash = ?
		ORDER BY ct.created_at, ct.id
		LIMIT 1`, hashToken(rawToken)).Scan(&ownerUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrClientTokenInvalid
	}
	if err != nil {
		return "", err
	}
	if !ownerUserID.Valid || ownerUserID.String == "" {
		return "", ErrUserOwnerUnavailable
	}
	return ownerUserID.String, nil
}

// ResolveClientKeyOwner is the read-only counterpart to key exchange. The key
// is validated again while the returned owner's lifecycle read gate is held.
func (s *AdminStore) ResolveClientKeyOwner(rawKey string) (string, error) {
	if rawKey == "" {
		return "", ErrClientKeyInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys, err := candidateAPIKeysForRaw(s.db, rawKey)
	if err != nil {
		return "", err
	}
	for _, key := range keys {
		if bcrypt.CompareHashAndPassword([]byte(key.KeyHash), []byte(rawKey)) != nil {
			continue
		}
		if key.OwnerUserID == "" {
			return "", ErrUserOwnerUnavailable
		}
		return key.OwnerUserID, nil
	}
	return "", ErrClientKeyInvalid
}
