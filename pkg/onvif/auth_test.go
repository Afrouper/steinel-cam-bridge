package onvif

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractUsernameToken(t *testing.T) {
	xmlSample := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">
  <s:Header>
    <wsse:Security>
      <wsse:UsernameToken>
        <wsse:Username>admin</wsse:Username>
        <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText">secret123</wsse:Password>
        <wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">bm9uY2UxMjM=</wsse:Nonce>
        <wsu:Created>2026-09-08T18:00:00Z</wsu:Created>
      </wsse:UsernameToken>
    </wsse:Security>
  </s:Header>
  <s:Body>
    <tds:GetDeviceInformation xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>
  </s:Body>
</s:Envelope>`

	tok, err := ExtractUsernameToken(xmlSample)
	require.NoError(t, err)
	require.NotNil(t, tok)
	assert.Equal(t, "admin", tok.Username)
	assert.Equal(t, "secret123", tok.Password.Value)
	assert.Contains(t, tok.Password.Type, "PasswordText")
	assert.Equal(t, "bm9uY2UxMjM=", tok.Nonce.Value)
	assert.Equal(t, "2026-09-08T18:00:00Z", tok.Created)
}

func TestExtractUsernameToken_MissingSecurityHeader(t *testing.T) {
	xmlNoAuth := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Header/>
  <s:Body>
    <tds:GetSystemDateAndTime xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>
  </s:Body>
</s:Envelope>`

	tok, err := ExtractUsernameToken(xmlNoAuth)
	assert.NoError(t, err)
	assert.Nil(t, tok)
}

func TestValidateWSSecurity_PasswordText(t *testing.T) {
	tok := &UsernameToken{
		Username: "admin",
		Password: PasswordElem{
			Type:  "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText",
			Value: "mysecret",
		},
	}

	// Valid credentials
	assert.True(t, ValidateWSSecurity(tok, "admin", "mysecret"))

	// Wrong password
	assert.False(t, ValidateWSSecurity(tok, "admin", "wrongsecret"))

	// Wrong user
	assert.False(t, ValidateWSSecurity(tok, "otheruser", "mysecret"))
}

func TestValidateWSSecurity_PasswordDigest(t *testing.T) {
	rawNonce := []byte("16_bytes_nonce__")
	b64Nonce := base64.StdEncoding.EncodeToString(rawNonce)
	created := "2026-09-08T18:00:00.000Z"
	password := "supersecret"

	// Compute expected digest: Base64(SHA1(rawNonce + created + password))
	h := sha1.New()
	h.Write(rawNonce)
	h.Write([]byte(created))
	h.Write([]byte(password))
	expectedDigest := base64.StdEncoding.EncodeToString(h.Sum(nil))

	tok := &UsernameToken{
		Username: "testuser",
		Password: PasswordElem{
			Type:  "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest",
			Value: expectedDigest,
		},
		Nonce: NonceElem{
			EncodingType: "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary",
			Value:        b64Nonce,
		},
		Created: created,
	}

	// Valid digest
	assert.True(t, ValidateWSSecurity(tok, "testuser", "supersecret"))

	// Wrong password
	assert.False(t, ValidateWSSecurity(tok, "testuser", "incorrectpassword"))

	// Wrong username
	assert.False(t, ValidateWSSecurity(tok, "admin", "supersecret"))
}

func TestValidateWSSecurity_FallbackMode(t *testing.T) {
	// Mode A: expectedUser is empty (Auth disabled on bridge)
	// Must accept requests without token
	assert.True(t, ValidateWSSecurity(nil, "", ""))

	// Must accept requests with dummy credentials
	dummyTok := &UsernameToken{
		Username: "admin",
		Password: PasswordElem{
			Value: "any_dummy_password",
		},
	}
	assert.True(t, ValidateWSSecurity(dummyTok, "", ""))
}

func TestFormatSOAPNotAuthorizedFault(t *testing.T) {
	faultXML := FormatSOAPNotAuthorizedFault()
	assert.Contains(t, faultXML, "s:Fault")
	assert.Contains(t, faultXML, "ter:NotAuthorized")
	assert.Contains(t, faultXML, "The security token could not be authenticated or authorized")
}

func TestNonceManager(t *testing.T) {
	mgr := NewNonceManager(2 * time.Second)
	nonce := mgr.Generate()
	require.NotEmpty(t, nonce)

	// Valid and fresh
	assert.Equal(t, AuthStatusSuccess, mgr.Validate(nonce))

	// Forged signature
	parts := strings.Split(nonce, "-")
	require.Len(t, parts, 3)
	forgedNonce := parts[0] + "-" + parts[1] + "-ffffffffffffffff"
	assert.Equal(t, AuthStatusFailed, mgr.Validate(forgedNonce))

	// Malformed nonce
	assert.Equal(t, AuthStatusFailed, mgr.Validate("malformed_nonce"))

	// Expired nonce
	expiredMgr := NewNonceManager(-1 * time.Second)
	expiredNonce := expiredMgr.Generate()
	assert.Equal(t, AuthStatusStale, expiredMgr.Validate(expiredNonce))
}

func TestParseDigestAuthorization(t *testing.T) {
	header := `Digest username="syno", realm="Steinel ONVIF Bridge", nonce="6aa3b864-12345678-97603f418ce972c3", uri="/onvif/device_service", response="6629fae49393a05397450978507c4ef1", qop=auth, nc=00000001, cnonce="0a4f113b", algorithm=MD5`
	params := ParseDigestAuthorization(header)
	require.NotNil(t, params)

	assert.Equal(t, "syno", params["username"])
	assert.Equal(t, "Steinel ONVIF Bridge", params["realm"])
	assert.Equal(t, "6aa3b864-12345678-97603f418ce972c3", params["nonce"])
	assert.Equal(t, "/onvif/device_service", params["uri"])
	assert.Equal(t, "6629fae49393a05397450978507c4ef1", params["response"])
	assert.Equal(t, "auth", params["qop"])
	assert.Equal(t, "00000001", params["nc"])
	assert.Equal(t, "0a4f113b", params["cnonce"])
	assert.Equal(t, "MD5", params["algorithm"])

	// Non-digest header
	assert.Nil(t, ParseDigestAuthorization("Basic dXNlcjpwYXNz"))
	assert.Nil(t, ParseDigestAuthorization(""))
}

func TestValidateDigestAuth_QopAuth(t *testing.T) {
	mgr := NewNonceManager(5 * time.Minute)
	nonce := mgr.Generate()

	user := "syno"
	pass := "secret123"
	realm := ONVIFAuthRealm
	method := "POST"
	uri := "/onvif/device_service"
	nc := "00000001"
	cnonce := "clientnonce123"
	qop := "auth"

	// HA1 = MD5(user:realm:pass)
	ha1 := md5Hex(fmt.Sprintf("%s:%s:%s", user, realm, pass))
	// HA2 = MD5(method:uri)
	ha2 := md5Hex(fmt.Sprintf("%s:%s", method, uri))
	// Response = MD5(HA1:nonce:nc:cnonce:qop:HA2)
	validResp := md5Hex(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, nonce, nc, cnonce, qop, ha2))

	validHeader := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", qop=%s, nc=%s, cnonce="%s"`,
		user, realm, nonce, uri, validResp, qop, nc, cnonce)

	// 1. Successful validation
	assert.Equal(t, AuthStatusSuccess, ValidateDigestAuth(method, uri, validHeader, user, pass, realm, mgr))

	// 2. Full URL in URI parameter (Synology / client sends absolute URI)
	fullURIHeader := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="http://192.168.1.50:8000%s", response="%s", qop=%s, nc=%s, cnonce="%s"`,
		user, realm, nonce, uri, md5Hex(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, nonce, nc, cnonce, qop, md5Hex(fmt.Sprintf("%s:http://192.168.1.50:8000%s", method, uri)))), qop, nc, cnonce)
	assert.Equal(t, AuthStatusSuccess, ValidateDigestAuth(method, uri, fullURIHeader, user, pass, realm, mgr))

	// 3. Wrong password
	wrongPassResp := md5Hex(fmt.Sprintf("%s:%s:%s:%s:%s:%s", md5Hex("syno:Steinel ONVIF Bridge:wrongpass"), nonce, nc, cnonce, qop, ha2))
	wrongPassHeader := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", qop=%s, nc=%s, cnonce="%s"`,
		user, realm, nonce, uri, wrongPassResp, qop, nc, cnonce)
	assert.Equal(t, AuthStatusFailed, ValidateDigestAuth(method, uri, wrongPassHeader, user, pass, realm, mgr))

	// 4. Wrong username
	wrongUserHeader := fmt.Sprintf(`Digest username="otheruser", realm="%s", nonce="%s", uri="%s", response="%s", qop=%s, nc=%s, cnonce="%s"`,
		realm, nonce, uri, validResp, qop, nc, cnonce)
	assert.Equal(t, AuthStatusFailed, ValidateDigestAuth(method, uri, wrongUserHeader, user, pass, realm, mgr))

	// 5. Stale nonce
	expiredMgr := NewNonceManager(-1 * time.Second)
	expiredNonce := expiredMgr.Generate()
	staleResp := md5Hex(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, expiredNonce, nc, cnonce, qop, ha2))
	staleHeader := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", qop=%s, nc=%s, cnonce="%s"`,
		user, realm, expiredNonce, uri, staleResp, qop, nc, cnonce)
	assert.Equal(t, AuthStatusStale, ValidateDigestAuth(method, uri, staleHeader, user, pass, realm, expiredMgr))

	// 6. Substring evasion attack prevention
	evilURI := "/onvif/evil_device_service"
	evilResp := md5Hex(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, nonce, nc, cnonce, qop, md5Hex(fmt.Sprintf("%s:%s", method, evilURI))))
	evilHeader := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", qop=%s, nc=%s, cnonce="%s"`,
		user, realm, nonce, evilURI, evilResp, qop, nc, cnonce)
	assert.Equal(t, AuthStatusFailed, ValidateDigestAuth(method, uri, evilHeader, user, pass, realm, mgr))
}

func TestValidateDigestAuth_LegacyNoQop(t *testing.T) {
	mgr := NewNonceManager(5 * time.Minute)
	nonce := mgr.Generate()

	user := "syno"
	pass := "secret123"
	realm := ONVIFAuthRealm
	method := "POST"
	uri := "/onvif/device_service"

	ha1 := md5Hex(fmt.Sprintf("%s:%s:%s", user, realm, pass))
	ha2 := md5Hex(fmt.Sprintf("%s:%s", method, uri))
	validResp := md5Hex(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))

	legacyHeader := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		user, realm, nonce, uri, validResp)

	assert.Equal(t, AuthStatusSuccess, ValidateDigestAuth(method, uri, legacyHeader, user, pass, realm, mgr))
}

func TestValidateDigestAuth_FallbackMode(t *testing.T) {
	// If expectedUser is empty, auth is disabled and any request passes
	assert.Equal(t, AuthStatusSuccess, ValidateDigestAuth("POST", "/onvif/device_service", "", "", "", "", nil))
}

func TestRedactAuthHeader(t *testing.T) {
	assert.Equal(t, "none", RedactAuthHeader(""))
	assert.Equal(t, "Basic", RedactAuthHeader("Basic dXNlcjpwYXNz"))
	assert.Equal(t, `Digest (user: "syno")`, RedactAuthHeader(`Digest username="syno", realm="test"`))
	assert.Equal(t, "Digest (no username)", RedactAuthHeader("Digest realm=\"test\""))
	assert.Equal(t, "Other", RedactAuthHeader("Bearer sometoken"))
}

func TestServer_SOAPDigestAuthRoundtrip(t *testing.T) {
	srv := NewServer(
		8000, 8554, "live", "aac", "de-test", "pr-test", "syno", "naspass123",
		nil, nil, nil, nil, nil, nil,
	)

	soapBody := `<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	// 1. Initial unauthenticated request -> returns 401 with Digest challenge and nonce
	req1 := httptest.NewRequest(http.MethodPost, "/onvif/device_service", strings.NewReader(soapBody))
	w1 := httptest.NewRecorder()
	srv.handleSOAP(w1, req1)

	assert.Equal(t, http.StatusUnauthorized, w1.Code)
	authHeaders := strings.Join(w1.Header().Values("WWW-Authenticate"), ", ")
	assert.Contains(t, authHeaders, `Digest realm="Steinel ONVIF Bridge"`)
	assert.Contains(t, authHeaders, `Basic realm="Steinel ONVIF Bridge"`)

	// Extract nonce from Digest challenge
	var nonce string
	for _, h := range w1.Header().Values("WWW-Authenticate") {
		if strings.HasPrefix(h, "Digest ") {
			params := ParseDigestAuthorization(h)
			nonce = params["nonce"]
			break
		}
	}
	require.NotEmpty(t, nonce, "Nonce must be returned in WWW-Authenticate header")

	// 2. Client sends valid Digest authorization
	method := "POST"
	uri := "/onvif/device_service"
	nc := "00000001"
	cnonce := "synoclient123"
	qop := "auth"
	ha1 := md5Hex(fmt.Sprintf("%s:%s:%s", "syno", ONVIFAuthRealm, "naspass123"))
	ha2 := md5Hex(fmt.Sprintf("%s:%s", method, uri))
	resp := md5Hex(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, nonce, nc, cnonce, qop, ha2))

	req2 := httptest.NewRequest(http.MethodPost, "/onvif/device_service", strings.NewReader(soapBody))
	req2.Header.Set("Authorization", fmt.Sprintf(`Digest username="syno", realm="%s", nonce="%s", uri="%s", response="%s", qop=%s, nc=%s, cnonce="%s"`,
		ONVIFAuthRealm, nonce, uri, resp, qop, nc, cnonce))
	w2 := httptest.NewRecorder()
	srv.handleSOAP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Contains(t, w2.Body.String(), "GetDeviceInformationResponse")

	// 3. Client sends invalid password -> returns 401
	wrongResp := md5Hex(fmt.Sprintf("%s:%s:%s:%s:%s:%s", md5Hex("syno:Steinel ONVIF Bridge:wrongpass"), nonce, nc, cnonce, qop, ha2))
	req3 := httptest.NewRequest(http.MethodPost, "/onvif/device_service", strings.NewReader(soapBody))
	req3.Header.Set("Authorization", fmt.Sprintf(`Digest username="syno", realm="%s", nonce="%s", uri="%s", response="%s", qop=%s, nc=%s, cnonce="%s"`,
		ONVIFAuthRealm, nonce, uri, wrongResp, qop, nc, cnonce))
	w3 := httptest.NewRecorder()
	srv.handleSOAP(w3, req3)

	assert.Equal(t, http.StatusUnauthorized, w3.Code)
	assert.Contains(t, w3.Body.String(), "ter:NotAuthorized")
}
