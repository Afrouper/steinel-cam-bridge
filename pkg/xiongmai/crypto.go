package xiongmai

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// DefaultSofiaAESKey is the pre-shared 128-bit AES key mandated by Xiongmai / JFTech Sofia firmware
// for pre-login security negotiation (MsgID 1413 / 1414) and outer login packet framing.
const DefaultSofiaAESKey = "dashoiahfarqdasr"

// encryptAES128CBC encrypts plaintext using AES-128-CBC with zero IV and PKCS#7 padding.
func encryptAES128CBC(key []byte, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	padLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padded := append(plaintext, bytes.Repeat([]byte{byte(padLen)}, padLen)...)

	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize))
	mode.CryptBlocks(ciphertext, padded)

	return ciphertext, nil
}

// decryptAES128CBC decrypts ciphertext using AES-128-CBC with zero IV and strips PKCS#7 / null padding.
func decryptAES128CBC(key []byte, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, errors.New("empty ciphertext")
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of block size %d", len(ciphertext), aes.BlockSize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	plaintext := make([]byte, len(ciphertext))
	mode := cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize))
	mode.CryptBlocks(plaintext, ciphertext)

	// Strip PKCS#7 padding if valid
	if len(plaintext) > 0 {
		padLen := int(plaintext[len(plaintext)-1])
		if padLen > 0 && padLen <= aes.BlockSize {
			valid := true
			for i := len(plaintext) - padLen; i < len(plaintext); i++ {
				if int(plaintext[i]) != padLen {
					valid = false
					break
				}
			}
			if valid {
				plaintext = plaintext[:len(plaintext)-padLen]
			}
		}
	}

	return bytes.TrimRight(plaintext, "\x00\r\n "), nil
}

// parseRSAPublicKey parses Xiongmai comma-separated hex modulus and exponent (e.g. "<ModulusHex>,010001").
func parseRSAPublicKey(pubKeyStr string) (*rsa.PublicKey, error) {
	parts := strings.Split(strings.TrimSpace(pubKeyStr), ",")
	if len(parts) == 0 || parts[0] == "" {
		return nil, errors.New("empty RSA public key")
	}

	modulusHex := strings.TrimSpace(parts[0])
	exponentHex := "010001"
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		exponentHex = strings.TrimSpace(parts[1])
	}

	n := new(big.Int)
	if _, ok := n.SetString(modulusHex, 16); !ok {
		return nil, fmt.Errorf("failed to parse RSA modulus from hex: %s", modulusHex)
	}

	e, err := strconv.ParseInt(exponentHex, 16, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RSA exponent from hex %s: %w", exponentHex, err)
	}

	return &rsa.PublicKey{
		N: n,
		E: int(e),
	}, nil
}

// encryptRSAPublicKey encrypts data using RSA PKCS#1 v1.5 padding and returns uppercase hex string.
//
// Required by Xiongmai hardware firmware protocol specification (EncryptAlgo: RSA_V1.5).
func encryptRSAPublicKey(pubKey *rsa.PublicKey, data []byte) (string, error) {
	//nolint:staticcheck // Mandated by Xiongmai hardware protocol specification (EncryptAlgo: RSA_V1.5)
	ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, pubKey, data)
	if err != nil {
		return "", fmt.Errorf("RSA PKCS1v15 encryption failed: %w", err)
	}
	return strings.ToUpper(hex.EncodeToString(ciphertext)), nil
}

// generateCommunicateKey generates a random 16-character alphanumeric string [0-9A-Za-z]
// as required by Xiongmai / JFTech NetIP protocol (CProtocolNetIP::NewLoginPTL).
func generateCommunicateKey() (string, error) {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	randBytes := make([]byte, 16)
	if _, err := rand.Read(randBytes); err != nil {
		return "", fmt.Errorf("failed to generate random communicate key: %w", err)
	}

	result := make([]byte, 16)
	for i := 0; i < 16; i++ {
		result[i] = charset[int(randBytes[i])%len(charset)]
	}
	return string(result), nil
}
