package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// envCredentials holds the camera user and password, obfuscated with the key below.
const envCredentials = "HIKVISION_CREDENTIALS"

// obfuscationKey keeps the camera credentials out of plain sight in the environment.
// It is embedded in the binary on purpose, so this is obfuscation and not secrecy:
// anyone holding the binary recovers the credentials, and so does anyone able to read
// /proc/<pid>/environ. What it does buy is that the password is not readable in the
// unit file, the deploy script, or a log dump that quotes the environment.
var obfuscationKey = []byte{
	0xef, 0x63, 0x27, 0x1c, 0x4d, 0x8a, 0x53, 0xf7,
	0xe5, 0xcb, 0x58, 0x62, 0x42, 0xda, 0x2b, 0x94,
	0x1f, 0xaa, 0xd5, 0xe9, 0xfd, 0xfa, 0x79, 0xd9,
	0xeb, 0xf9, 0x24, 0x92, 0xb1, 0xe3, 0x29, 0x42,
}

// credentialsSeparator cannot appear in a user name or password, so it is safe to
// join both fields with it.
const credentialsSeparator = "\x00"

func newGCM() (cipher.AEAD, error) {
	block, err := aes.NewCipher(obfuscationKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// encryptCredentials builds the value to store in HIKVISION_CREDENTIALS.
func encryptCredentials(user, pass string) (string, error) {
	if strings.Contains(user, credentialsSeparator) || strings.Contains(pass, credentialsSeparator) {
		return "", fmt.Errorf("user and password cannot contain a NUL byte")
	}
	if len(user) == 0 || len(pass) == 0 {
		return "", fmt.Errorf("user and password cannot be empty")
	}
	gcm, err := newGCM()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(user+credentialsSeparator+pass), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// decryptCredentials recovers the pair stored by encryptCredentials.
func decryptCredentials(blob string) (string, string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(blob))
	if err != nil {
		return "", "", fmt.Errorf("value is not valid base64: %w", err)
	}
	gcm, err := newGCM()
	if err != nil {
		return "", "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", "", fmt.Errorf("value is too short to be valid")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		// Wrong key or corrupted value; never echo the input, it may be half a secret.
		return "", "", fmt.Errorf("value cannot be decrypted, regenerate it with -encryptCredentials")
	}
	parts := strings.SplitN(string(plain), credentialsSeparator, 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("decrypted value has no user/password separator")
	}
	return parts[0], parts[1], nil
}

// credentialsFromEnv reads and decodes HIKVISION_CREDENTIALS. Missing credentials are
// not an error: only the camera dialogue needs them, and the counting must keep
// working without them.
func credentialsFromEnv() (user, pass string, err error) {
	blob := os.Getenv(envCredentials)
	if len(blob) == 0 {
		return "", "", nil
	}
	return decryptCredentials(blob)
}

// printEncryptedCredentials reads a user and a password from stdin, one per line, and
// prints the value for the environment variable. Reading from stdin instead of flags
// keeps the password out of the process list.
func printEncryptedCredentials() int {
	var user, pass string
	if _, err := fmt.Fscanln(os.Stdin, &user); err != nil {
		fmt.Fprintf(os.Stderr, "error leyendo el usuario: %s\n", err)
		return 1
	}
	if _, err := fmt.Fscanln(os.Stdin, &pass); err != nil {
		fmt.Fprintf(os.Stderr, "error leyendo la clave: %s\n", err)
		return 1
	}
	blob, err := encryptCredentials(user, pass)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	fmt.Printf("%s=%s\n", envCredentials, blob)
	return 0
}
