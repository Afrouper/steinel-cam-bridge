package onvif

import (
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/xml"
	"strings"
)

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
