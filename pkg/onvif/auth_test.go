package onvif

import (
	"crypto/sha1"
	"encoding/base64"
	"testing"

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
