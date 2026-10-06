package provider

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Web push message encryption (RFC 8291, aes128gcm from RFC 8188) and VAPID
// authorization (RFC 8292).

// WebPushMaxRecord is the largest encrypted body push services must accept.
const WebPushMaxRecord = 4096

const webPushHeaderLen = 16 + 4 + 1 + 65 // salt, record size, key id length, key id

var ErrWebPushTooLarge = errors.New("push payload is too large")

// VAPIDKeys is the server's application key pair.
type VAPIDKeys struct {
	Public  string // base64url uncompressed P-256 point, as given to browsers
	KeyID   string // first 16 hex characters of sha256(public point)
	private *ecdsa.PrivateKey
}

func decodeB64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
}

// ParseVAPIDKeys checks that the base64url private scalar and public point
// form one P-256 key pair.
func ParseVAPIDKeys(public, private string) (*VAPIDKeys, error) {
	pub, err := decodeB64URL(public)
	if err != nil || len(pub) != 65 || pub[0] != 4 {
		return nil, errors.New("public key must be a base64url 65-byte uncompressed P-256 point")
	}
	d, err := decodeB64URL(private)
	if err != nil || len(d) != 32 {
		return nil, errors.New("private key must be a base64url 32-byte P-256 scalar")
	}
	key, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return nil, errors.New("private key is not a valid P-256 scalar")
	}
	if string(key.PublicKey().Bytes()) != string(pub) {
		return nil, errors.New("public key does not match the private key")
	}
	sum := sha256.Sum256(pub)
	return &VAPIDKeys{
		Public: base64.RawURLEncoding.EncodeToString(pub),
		KeyID:  hex.EncodeToString(sum[:])[:16],
		private: &ecdsa.PrivateKey{
			PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(pub[1:33]), Y: new(big.Int).SetBytes(pub[33:])},
			D:         new(big.Int).SetBytes(d),
		},
	}, nil
}

// GenerateVAPIDKeys returns a new key pair as base64url (public point, private scalar).
func GenerateVAPIDKeys() (public, private string, err error) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(key.Bytes()), nil
}

// DecodeSubscriptionKeys validates a browser subscription's p256dh point and
// auth secret (base64url).
func DecodeSubscriptionKeys(p256dh, auth string) (uaPublic, authSecret []byte, err error) {
	uaPublic, err = decodeB64URL(p256dh)
	if err != nil || len(uaPublic) != 65 {
		return nil, nil, errors.New("p256dh key must be a base64url 65-byte P-256 point")
	}
	if _, err = ecdh.P256().NewPublicKey(uaPublic); err != nil {
		return nil, nil, errors.New("p256dh key is not a valid P-256 point")
	}
	authSecret, err = decodeB64URL(auth)
	if err != nil || len(authSecret) != 16 {
		return nil, nil, errors.New("auth key must be a base64url 16-byte secret")
	}
	return uaPublic, authSecret, nil
}

// EncryptWebPush encrypts plaintext for one subscription as a single
// aes128gcm record with a fresh ephemeral key and salt.
func EncryptWebPush(uaPublic, authSecret, plaintext []byte) ([]byte, error) {
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptWebPush(as, salt, uaPublic, authSecret, plaintext)
}

func encryptWebPush(as *ecdh.PrivateKey, salt, uaPublic, authSecret, plaintext []byte) ([]byte, error) {
	if webPushHeaderLen+len(plaintext)+1+16 > WebPushMaxRecord {
		return nil, ErrWebPushTooLarge
	}
	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("invalid subscription key: %w", err)
	}
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := as.PublicKey().Bytes()
	// RFC 8291 §3.3: combine the ECDH secret with the auth secret, then derive
	// the content key and nonce (RFC 8188 §2.2) from the salt.
	prkKey, err := hkdf.Extract(sha256.New, shared, authSecret)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPublic)+string(asPublic), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, webPushHeaderLen+len(plaintext)+17)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, WebPushMaxRecord)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	// A single, last record: the 0x02 delimiter and no padding.
	return gcm.Seal(out, nonce, append(append([]byte{}, plaintext...), 2), nil), nil
}

// VAPIDAuthorization returns the Authorization header value for a push to
// endpoint: an ES256 JWT for the endpoint's origin, valid for 12 hours.
func (k *VAPIDKeys) VAPIDAuthorization(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid push endpoint")
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": subject,
	}).SignedString(k.private)
	if err != nil {
		return "", err
	}
	return "vapid t=" + token + ", k=" + k.Public, nil
}

// VerifyingKey is the public key that checks VAPID signatures.
func (k *VAPIDKeys) VerifyingKey() *ecdsa.PublicKey { return &k.private.PublicKey }
