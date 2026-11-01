package netease

// 网易云音乐 weapi 加密（复刻自 MusicForUrl 的 netease.js）。
//
// weapi 的流程：
//  1. first   = base64(AES-CBC(JSON(data), PRESET_KEY, IV))
//  2. secret  = 16 个随机 base62 字符
//  3. params  = base64(AES-CBC(first, secret, IV))
//  4. encSecKey = hex(RSA-PKCS1v15(secret 反转, PUBLIC_KEY))
//
// eapi 的流程（部分接口用）：
//   params = base64(AES-ECB("<uri>-36cd479b6b5-<body>-36cd479b6b5-<md5>", EAPI_KEY))

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"strings"
)

const (
	presetKey  = "0CoJUm6Qyw8W8jud"
	eapiKey    = "e82ckenh8dichen8"
	aesIV      = "0102030405060708"
	base62     = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	eapiSep    = "-36cd479b6b5-"
	webDomain  = "https://music.163.com"
	eapiDomain = "https://interface.music.163.com"
)

const publicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDgtQn2JZ34ZC28NWYpAUd98iZ37BUrX/aKzmFbt7clFSs6sXqHauqKWqdtLkF2KexO40H1YTX8z2lSgBBOAxLsvaklV8k4cBFK9snQXE9/DDaFt6Rr7iVZMldczhC0JNgTz+SHXT6CBHuX3e9SdB1Ua44oncaTWz7OBGLbCiK45wIDAQAB
-----END PUBLIC KEY-----`

var cachedPub *rsa.PublicKey

func loadPublicKey() (*rsa.PublicKey, error) {
	if cachedPub != nil {
		return cachedPub, nil
	}
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("解析网易云公钥失败")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("网易云公钥类型异常")
	}
	cachedPub = rsaPub
	return rsaPub, nil
}

// aesEncryptBase64 AES-CBC/PKCS7，返回 base64。
func aesEncryptBase64(plain, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	bs := block.BlockSize()
	pad := bs - len(plain)%bs
	buf := make([]byte, len(plain)+pad)
	copy(buf, plain)
	for i := len(plain); i < len(buf); i++ {
		buf[i] = byte(pad)
	}
	out := make([]byte, len(buf))
	cipher.NewCBCEncrypter(block, []byte(aesIV)).CryptBlocks(out, buf)
	return base64.StdEncoding.EncodeToString(out), nil
}

// aesEncryptECBHex AES-ECB/PKCS7，返回大写 hex（eapi 用）。
func aesEncryptECBHex(plain, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	bs := block.BlockSize()
	pad := bs - len(plain)%bs
	buf := make([]byte, len(plain)+pad)
	copy(buf, plain)
	for i := len(plain); i < len(buf); i++ {
		buf[i] = byte(pad)
	}
	out := make([]byte, len(buf))
	for i := 0; i < len(buf); i += bs {
		block.Encrypt(out[i:i+bs], buf[i:i+bs])
	}
	return strings.ToUpper(hex.EncodeToString(out)), nil
}

func randomSecret() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = byte(i * 7)
		}
	}
	out := make([]byte, 16)
	for i, b := range buf {
		out[i] = base62[int(b)%len(base62)]
	}
	return string(out)
}

// weapiForm 构造 params + encSecKey。
func weapiForm(data map[string]any) (url.Values, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	first, err := aesEncryptBase64(raw, []byte(presetKey))
	if err != nil {
		return nil, err
	}
	secret := randomSecret()
	params, err := aesEncryptBase64([]byte(first), []byte(secret))
	if err != nil {
		return nil, err
	}

	pub, err := loadPublicKey()
	if err != nil {
		return nil, err
	}
	// secret 反转后再 RSA 加密
	rev := []byte(reverseString(secret))
	encSecret, err := rsa.EncryptPKCS1v15(rand.Reader, pub, rev)
	if err != nil {
		return nil, err
	}

	v := url.Values{}
	v.Set("params", params)
	v.Set("encSecKey", hex.EncodeToString(encSecret))
	return v, nil
}

// eapiForm 构造 eapi 的 params。
func eapiForm(uri string, data map[string]any) (url.Values, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	sum := md5.Sum([]byte("nobody" + uri + "use" + string(body) + "md5forencrypt"))
	text := uri + eapiSep + string(body) + eapiSep + hex.EncodeToString(sum[:])
	params, err := aesEncryptECBHex([]byte(text), []byte(eapiKey))
	if err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("params", params)
	return v, nil
}

func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
