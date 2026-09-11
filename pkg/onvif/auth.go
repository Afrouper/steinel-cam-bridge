package onvif

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ONVIFAuthRealm is the HTTP Digest and Basic authentication realm for ONVIF services.
const ONVIFAuthRealm = "Steinel ONVIF Bridge"

var digestParamRegex = regexp.MustCompile(`(?i)([a-z0-9_-]+)\s*=\s*(?:"([^"]*)"|([^\s,]+))`)

// NonceManager handles generation and stateless cryptographic validation of HTTP Digest nonces.
type NonceManager struct {
	secret []byte
	ttl    time.Duration
}

// NewNonceManager creates a NonceManager with a random secret and the given TTL.
func NewNonceManager(ttl time.Duration) *NonceManager {
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		binary.BigEndian.PutUint64(secret[:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(secret[8:], uint64(time.Now().UnixNano()^0x5a5a5a5a))
	}
	return &NonceManager{
		secret: secret,
		ttl:    ttl,
	}
}

// Generate creates a cryptographically authenticated timestamp nonce (hex-encoded).
func (m *NonceManager) Generate() string {
	now := time.Now().Unix()
	h := hmac.New(sha256.New, m.secret)
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(now))
	h.Write(buf)
	sig := h.Sum(nil)[:8]
	return fmt.Sprintf("%x-%x", now, sig)
}

// Validate checks if the nonce was issued by this server and whether it is expired.
// Returns valid=true if authentic and within TTL, stale=true if authentic but expired.
func (m *NonceManager) Validate(nonce string) (valid bool, stale bool) {
	parts := strings.Split(nonce, "-")
	if len(parts) != 2 {
		return false, false
	}
	nowInt, err := strconv.ParseInt(parts[0], 16, 64)
	if err != nil {
		return false, false
	}
	sigBytes, err := hex.DecodeString(parts[1])
	if err != nil || len(sigBytes) != 8 {
		return false, false
	}

	h := hmac.New(sha256.New, m.secret)
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(nowInt))
	h.Write(buf)
	expectedSig := h.Sum(nil)[:8]

	if subtle.ConstantTimeCompare(sigBytes, expectedSig) != 1 {
		return false, false
	}

	age := time.Since(time.Unix(nowInt, 0))
	if age < 0 || age > m.ttl {
		return false, true
	}

	return true, false
}

// ParseDigestAuthorization parses the key-value pairs from an HTTP Authorization: Digest header.
func ParseDigestAuthorization(header string) map[string]string {
	trimmed := strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(trimmed), "digest ") {
		return nil
	}
	raw := strings.TrimSpace(trimmed[7:])
	matches := digestParamRegex.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return nil
	}

	params := make(map[string]string, len(matches))
	for _, m := range matches {
		key := strings.ToLower(m[1])
		val := m[2]
		if val == "" {
			val = m[3]
		}
		params[key] = val
	}
	return params
}

// ValidateDigestAuth validates an HTTP Digest Authorization header against expected credentials.
// Returns valid=true if authentication succeeded, stale=true if nonce was authentic but expired.
func ValidateDigestAuth(
	method string,
	reqURI string,
	authHeader string,
	expectedUser string,
	expectedPass string,
	expectedRealm string,
	nonceMgr *NonceManager,
) (valid bool, stale bool) {
	if expectedUser == "" {
		return true, false
	}

	params := ParseDigestAuthorization(authHeader)
	if params == nil {
		return false, false
	}

	// 1. Verify username
	username := params["username"]
	if subtle.ConstantTimeCompare([]byte(username), []byte(expectedUser)) != 1 {
		return false, false
	}

	// 2. Verify realm (if present in params, must match expectedRealm)
	if realm, ok := params["realm"]; ok && realm != "" {
		if subtle.ConstantTimeCompare([]byte(realm), []byte(expectedRealm)) != 1 {
			return false, false
		}
	}

	// 3. Verify algorithm (empty, MD5 or md5 supported)
	if alg, ok := params["algorithm"]; ok && alg != "" {
		if !strings.EqualFold(alg, "MD5") {
			return false, false
		}
	}

	// 4. Verify nonce
	nonce := params["nonce"]
	if nonceMgr != nil {
		nValid, nStale := nonceMgr.Validate(nonce)
		if !nValid {
			return false, nStale
		}
	}

	// 5. Verify URI
	clientURI := params["uri"]
	if clientURI == "" {
		return false, false
	}
	if clientURI != reqURI && !strings.HasSuffix(clientURI, reqURI) && !strings.Contains(clientURI, reqURI) {
		return false, false
	}

	// 6. Compute HA1 = MD5(username:realm:password)
	realmToUse := expectedRealm
	if r, ok := params["realm"]; ok && r != "" {
		realmToUse = r
	}
	ha1 := md5Hex(fmt.Sprintf("%s:%s:%s", expectedUser, realmToUse, expectedPass))

	// 7. Compute HA2 = MD5(method:digestURI)
	ha2 := md5Hex(fmt.Sprintf("%s:%s", method, clientURI))

	// 8. Compute Expected Response
	qop := strings.ToLower(params["qop"])
	var expectedResp string
	switch qop {
	case "auth", "auth-int":
		nc := params["nc"]
		cnonce := params["cnonce"]
		expectedResp = md5Hex(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, nonce, nc, cnonce, qop, ha2))
	case "":
		expectedResp = md5Hex(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))
	default:
		return false, false
	}

	clientResp := strings.ToLower(params["response"])
	if subtle.ConstantTimeCompare([]byte(clientResp), []byte(expectedResp)) != 1 {
		return false, false
	}

	return true, false
}

func md5Hex(s string) string {
	h := md5.New()
	_, _ = io.WriteString(h, s)
	return hex.EncodeToString(h.Sum(nil))
}

// RedactAuthHeader returns a safe representation of the Authorization header for logging without revealing secrets.
func RedactAuthHeader(authHeader string) string {
	trimmed := strings.TrimSpace(authHeader)
	if trimmed == "" {
		return "none"
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "digest ") {
		params := ParseDigestAuthorization(authHeader)
		if user, ok := params["username"]; ok && user != "" {
			return fmt.Sprintf("Digest (user: %q)", user)
		}
		return "Digest (no username)"
	}
	if strings.HasPrefix(lower, "basic ") {
		return "Basic"
	}
	return "Other"
}

// UsernameToken represents the WS-Security UsernameToken element.
type UsernameToken struct {
	Username string       `xml:"Username"`
	Password PasswordElem `xml:"Password"`
	Nonce    NonceElem    `xml:"Nonce"`
	Created  string       `xml:"Created"`
}

// PasswordElem represents the Password element inside a UsernameToken.
type PasswordElem struct {
	Type  string `xml:"Type,attr"`
	Value string `xml:",chardata"`
}

// NonceElem represents the Nonce element inside a UsernameToken.
type NonceElem struct {
	EncodingType string `xml:"EncodingType,attr"`
	Value        string `xml:",chardata"`
}

// soapHeaderEnvelope is used to extract the WS-Security Header.
type soapHeaderEnvelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Header  struct {
		Security struct {
			UsernameToken *UsernameToken `xml:"UsernameToken"`
		} `xml:"Security"`
	} `xml:"Header"`
}

// ExtractUsernameToken extracts the WS-Security UsernameToken from a SOAP request XML string.
// Returns nil if no Security header or UsernameToken is present.
func ExtractUsernameToken(reqXML string) (*UsernameToken, error) {
	var env soapHeaderEnvelope
	if err := xml.Unmarshal([]byte(reqXML), &env); err != nil {
		return nil, err
	}
	return env.Header.Security.UsernameToken, nil
}

// ValidateWSSecurity validates the extracted UsernameToken against expected credentials.
// If expectedUser is empty, authentication is disabled and any request (including dummy credentials) is accepted.
func ValidateWSSecurity(token *UsernameToken, expectedUser, expectedPass string) bool {
	// Mode A: Fallback / Unauthenticated mode -> always allow
	if expectedUser == "" {
		return true
	}

	// Mode B: Authenticated mode -> token must be present
	if token == nil {
		return false
	}

	trimmedUser := strings.TrimSpace(token.Username)
	trimmedPassVal := strings.TrimSpace(token.Password.Value)
	trimmedPassType := strings.TrimSpace(token.Password.Type)

	if subtle.ConstantTimeCompare([]byte(trimmedUser), []byte(expectedUser)) != 1 {
		return false
	}

	// 1. PasswordDigest validation: Base64( SHA-1( B64Decode(Nonce) + Created + Password ) )
	if strings.Contains(trimmedPassType, "PasswordDigest") {
		trimmedNonce := strings.TrimSpace(token.Nonce.Value)
		rawNonce, err := base64.StdEncoding.DecodeString(trimmedNonce)
		if err != nil {
			rawNonce = []byte(trimmedNonce)
		}

		h := sha1.New()
		h.Write(rawNonce)
		h.Write([]byte(strings.TrimSpace(token.Created)))
		h.Write([]byte(expectedPass))
		expectedDigest := base64.StdEncoding.EncodeToString(h.Sum(nil))

		return subtle.ConstantTimeCompare([]byte(trimmedPassVal), []byte(expectedDigest)) == 1
	}

	// 2. PasswordText validation (plain text comparison)
	return subtle.ConstantTimeCompare([]byte(trimmedPassVal), []byte(expectedPass)) == 1
}

// FormatSOAPNotAuthorizedFault returns an ONVIF-compliant SOAP 1.2 NotAuthorized fault.
func FormatSOAPNotAuthorizedFault() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:ter="http://www.onvif.org/ver10/error">
  <s:Body>
    <s:Fault>
      <s:Code>
        <s:Value>s:Sender</s:Value>
        <s:Subcode>
          <s:Value>ter:NotAuthorized</s:Value>
        </s:Subcode>
      </s:Code>
      <s:Reason>
        <s:Text xml:lang="en">The security token could not be authenticated or authorized</s:Text>
      </s:Reason>
    </s:Fault>
  </s:Body>
</s:Envelope>`
}
