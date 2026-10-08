package notifications

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// SignatureHeader carries a signed webhook delivery's signature: "sha256="
// followed by the hex HMAC-SHA256 of the request body under the channel's
// signing secret — the same scheme GitHub uses for X-Hub-Signature-256, so
// receivers can reuse that verification code.
const SignatureHeader = "X-Spacefleet-Signature-256"

// Sign computes the SignatureHeader value for body under secret.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature reports whether sig is Sign(secret, body), in constant
// time. Exported for receivers written in Go (and the tests).
func VerifySignature(secret string, body []byte, sig string) bool {
	return hmac.Equal([]byte(sig), []byte(Sign(secret, body)))
}
