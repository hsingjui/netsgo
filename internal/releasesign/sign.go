package releasesign

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

func PrivateKeyFromPEM(raw []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("decode PEM private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS8 private key: %w", err)
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not Ed25519")
	}
	return edKey, nil
}

func RawSignature(privateKey ed25519.PrivateKey, data []byte) []byte {
	return ed25519.Sign(privateKey, data)
}
