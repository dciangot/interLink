package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// SSHKeyPair holds the generated SSH key pair
type SSHKeyPair struct {
	PrivateKey       string
	PublicKey        string
	PublicKeyOpenSSH string
}

// GenerateSSHKeyPair generates a new RSA SSH key pair
func GenerateSSHKeyPair() (*SSHKeyPair, error) {
	// Generate RSA private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %v", err)
	}

	// Encode private key to PEM format
	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}
	privateKeyBytes := pem.EncodeToMemory(privateKeyPEM)

	// Generate public key
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to generate public key: %v", err)
	}

	// Format public key for OpenSSH
	publicKeyOpenSSH := string(ssh.MarshalAuthorizedKey(publicKey))

	// Format public key for SSH (same as OpenSSH but without newline)
	publicKeySSH := fmt.Sprintf("%s %s interlink-webui-generated",
		publicKey.Type(),
		string(publicKey.Marshal()))

	return &SSHKeyPair{
		PrivateKey:       string(privateKeyBytes),
		PublicKey:        publicKeySSH,
		PublicKeyOpenSSH: publicKeyOpenSSH,
	}, nil
}
