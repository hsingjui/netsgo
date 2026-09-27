package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
)

func TestAPIPasskeyLoginFinishCreatesSessionAndPersistsCounter(t *testing.T) {
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()
	handler := s.StartHTTPOnly()

	user, err := s.auth.adminStore.ValidateUserPassword("admin", "password123")
	if err != nil {
		t.Fatalf("load administrator: %v", err)
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate passkey key pair: %v", err)
	}
	publicKey, err := webauthncbor.Marshal(map[int64]any{
		1:  int64(webauthncose.EllipticKey),
		3:  int64(webauthncose.AlgES256),
		-1: int64(webauthncose.P256),
		-2: privateKey.X.FillBytes(make([]byte, 32)),
		-3: privateKey.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatalf("encode passkey public key: %v", err)
	}
	credentialID := make([]byte, 32)
	if _, err := rand.Read(credentialID); err != nil {
		t.Fatalf("generate credential id: %v", err)
	}
	storedCredential := webauthn.Credential{
		ID:        credentialID,
		PublicKey: publicKey,
		Flags: webauthn.CredentialFlags{
			UserPresent:  true,
			UserVerified: true,
		},
	}
	actor := ActivityActor{Type: "admin", ID: user.ID, Name: user.Username}
	if _, _, err := s.auth.adminStore.AddPasskeyWithActivity(
		user.ID,
		"login-finish-test",
		credentialIDString(credentialID),
		storedCredential,
		"localhost",
		"http://localhost",
		actor,
	); err != nil {
		t.Fatalf("store passkey: %v", err)
	}

	beginReq := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/begin", nil)
	beginReq.Host = "localhost"
	beginReq.Header.Set("Origin", "http://localhost")
	beginResp := httptest.NewRecorder()
	handler.ServeHTTP(beginResp, beginReq)
	if beginResp.Code != http.StatusOK {
		t.Fatalf("passkey login begin: status=%d body=%s", beginResp.Code, beginResp.Body.String())
	}
	var begin struct {
		ChallengeID string `json:"challenge_id"`
	}
	if err := json.Unmarshal(beginResp.Body.Bytes(), &begin); err != nil {
		t.Fatalf("decode passkey begin response: %v", err)
	}
	challenge, err := s.auth.adminStore.GetAuthChallenge(begin.ChallengeID, adminAuthChallengeKindPasskeyLogin)
	if err != nil {
		t.Fatalf("load passkey challenge: %v", err)
	}
	session, err := unmarshalWebAuthnSession(challenge.SessionJSON)
	if err != nil {
		t.Fatalf("decode passkey session: %v", err)
	}

	clientDataJSON, err := json.Marshal(map[string]any{
		"type":        "webauthn.get",
		"challenge":   session.Challenge,
		"origin":      "http://localhost",
		"crossOrigin": false,
	})
	if err != nil {
		t.Fatalf("encode passkey client data: %v", err)
	}
	rpIDHash := sha256.Sum256([]byte("localhost"))
	authenticatorData := make([]byte, 37)
	copy(authenticatorData, rpIDHash[:])
	authenticatorData[32] = 0x05 // user present and user verified
	binary.BigEndian.PutUint32(authenticatorData[33:], 1)
	clientDataHash := sha256.Sum256(clientDataJSON)
	signedData := append(append([]byte{}, authenticatorData...), clientDataHash[:]...)
	signedDataHash := sha256.Sum256(signedData)
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, signedDataHash[:])
	if err != nil {
		t.Fatalf("sign passkey assertion: %v", err)
	}
	base64URL := base64.RawURLEncoding.EncodeToString
	credential := map[string]any{
		"id":    base64URL(credentialID),
		"rawId": base64URL(credentialID),
		"type":  "public-key",
		"response": map[string]any{
			"authenticatorData": base64URL(authenticatorData),
			"clientDataJSON":    base64URL(clientDataJSON),
			"signature":         base64URL(signature),
			"userHandle":        base64URL([]byte(user.ID)),
		},
	}
	finishBody, err := json.Marshal(map[string]any{
		"challenge_id": begin.ChallengeID,
		"credential":   credential,
	})
	if err != nil {
		t.Fatalf("encode passkey finish request: %v", err)
	}
	finishReq := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/finish", bytes.NewReader(finishBody))
	finishReq.Host = "localhost"
	finishReq.Header.Set("Content-Type", "application/json")
	finishReq.Header.Set("Origin", "http://localhost")
	finishResp := httptest.NewRecorder()
	handler.ServeHTTP(finishResp, finishReq)
	if finishResp.Code != http.StatusOK {
		t.Fatalf("passkey login finish: status=%d body=%s", finishResp.Code, finishResp.Body.String())
	}
	var auth authSuccessPayload
	if err := json.Unmarshal(finishResp.Body.Bytes(), &auth); err != nil {
		t.Fatalf("decode passkey login response: %v", err)
	}
	if auth.Token == "" || auth.User.ID != user.ID {
		t.Fatalf("passkey login response = %+v", auth)
	}
	if _, err := s.auth.adminStore.GetAuthChallenge(begin.ChallengeID, adminAuthChallengeKindPasskeyLogin); err == nil {
		t.Fatal("successful passkey login did not consume its challenge")
	}
	passkeys, err := s.auth.adminStore.ListPasskeys(user.ID)
	if err != nil {
		t.Fatalf("reload passkey: %v", err)
	}
	if len(passkeys) != 1 || passkeys[0].LastUsedAt == nil {
		t.Fatalf("passkey usage metadata = %+v", passkeys)
	}
	updatedCredential, err := passkeys[0].WebAuthnCredential()
	if err != nil {
		t.Fatalf("decode updated passkey: %v", err)
	}
	if updatedCredential.Authenticator.SignCount != 1 || updatedCredential.Authenticator.CloneWarning {
		t.Fatalf("updated authenticator = %+v", updatedCredential.Authenticator)
	}
}
