package provider

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	out, err := base64.RawURLEncoding.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// RFC 8291 §5 and Appendix A.
const (
	rfcASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcMessage   = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func TestEncryptWebPushRFC8291Vector(t *testing.T) {
	as, err := ecdh.P256().NewPrivateKey(b64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	got, err := encryptWebPush(as, b64(t, rfcSalt), b64(t, rfcUAPublic), b64(t, rfcAuth), []byte("When I grow up, I want to be a watermelon"))
	if err != nil {
		t.Fatal(err)
	}
	if enc := base64.RawURLEncoding.EncodeToString(got); enc != rfcMessage {
		t.Fatalf("ciphertext mismatch:\n got %s\nwant %s", enc, rfcMessage)
	}
}

// decryptWebPush is the user agent's side, used to check random-key output.
func decryptWebPush(t *testing.T, uaPrivate *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	salt, rs, idLen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asPublic := body[21 : 21+idLen]
	if rs != WebPushMaxRecord || idLen != 65 {
		t.Fatalf("header rs=%d idlen=%d", rs, idLen)
	}
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := uaPrivate.ECDH(as)
	prkKey, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPrivate.PublicKey().Bytes())+string(asPublic), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idLen:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatal("missing last-record delimiter")
	}
	return plain[:len(plain)-1]
}

func TestEncryptWebPushRoundTripAndLimit(t *testing.T) {
	ua, err := ecdh.P256().NewPrivateKey(b64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	uaPub, auth, err := DecodeSubscriptionKeys(rfcUAPublic, rfcAuth)
	if err != nil {
		t.Fatal(err)
	}
	body, err := EncryptWebPush(uaPub, auth, []byte(`{"type":"new_email"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := decryptWebPush(t, ua, auth, body); string(got) != `{"type":"new_email"}` {
		t.Fatalf("round trip %q", got)
	}
	again, _ := EncryptWebPush(uaPub, auth, []byte(`{"type":"new_email"}`))
	if string(again[:16]) == string(body[:16]) || string(again[21:86]) == string(body[21:86]) {
		t.Fatal("salt and ephemeral key must be fresh per message")
	}
	maxPlain := WebPushMaxRecord - webPushHeaderLen - 17
	if body, err = EncryptWebPush(uaPub, auth, make([]byte, maxPlain)); err != nil || len(body) != WebPushMaxRecord {
		t.Fatalf("largest payload: %d %v", len(body), err)
	}
	if _, err = EncryptWebPush(uaPub, auth, make([]byte, maxPlain+1)); !errors.Is(err, ErrWebPushTooLarge) {
		t.Fatalf("oversize payload: %v", err)
	}
	for _, keys := range [][2]string{{rfcUAPublic[:40], rfcAuth}, {rfcUAPublic, rfcAuth + "AA"}, {"B" + strings.Repeat("A", 86), rfcAuth}} {
		if _, _, err = DecodeSubscriptionKeys(keys[0], keys[1]); err == nil {
			t.Fatalf("accepted invalid subscription keys %v", keys)
		}
	}
}

func TestVAPIDKeysAndAuthorization(t *testing.T) {
	keys, err := ParseVAPIDKeys(rfcASPublic, rfcASPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.KeyID) != 16 || keys.Public != rfcASPublic {
		t.Fatalf("key id %q public %q", keys.KeyID, keys.Public)
	}
	for _, pair := range [][2]string{{rfcUAPublic, rfcASPrivate}, {rfcASPublic, "short"}, {"bad", rfcASPrivate}} {
		if _, err = ParseVAPIDKeys(pair[0], pair[1]); err == nil || strings.Contains(err.Error(), rfcASPrivate) {
			t.Fatalf("pair %v: %v", pair, err)
		}
	}
	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseVAPIDKeys(pub, priv); err != nil {
		t.Fatalf("generated keys do not parse: %v", err)
	}

	now := time.Now()
	header, err := keys.VAPIDAuthorization("https://fcm.googleapis.com/fcm/send/abc?x=1", "mailto:ops@example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	token, k, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	if !ok || !strings.HasPrefix(header, "vapid t=") || k != rfcASPublic {
		t.Fatalf("header %q", header)
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return keys.VerifyingKey(), nil }, jwt.WithValidMethods([]string{"ES256"}))
	if err != nil || !parsed.Valid {
		t.Fatalf("signature: %v", err)
	}
	exp, _ := claims.GetExpirationTime()
	if claims["aud"] != "https://fcm.googleapis.com" || claims["sub"] != "mailto:ops@example.com" || exp.Unix() != now.Add(12*time.Hour).Unix() {
		t.Fatalf("claims %v", claims)
	}
}
