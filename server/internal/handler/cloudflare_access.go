package handler

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
)

type cloudflareAccessClaims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

type cloudflareJWKSet struct {
	Keys []cloudflareJWK `json:"keys"`
}

type cloudflareJWK struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

var cloudflareHTTPClient = &http.Client{Timeout: 5 * time.Second}

func cloudflareIssuer() (string, error) {
	issuer := strings.TrimRight(strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCESS_ISSUER")), "/")
	if issuer == "" {
		return "", errors.New("CLOUDFLARE_ACCESS_ISSUER is not configured")
	}
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid Cloudflare Access issuer")
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".cloudflareaccess.com") {
		return "", errors.New("Cloudflare Access issuer must use cloudflareaccess.com")
	}
	return issuer, nil
}

func cloudflareAllowedEmail(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, allowed := range strings.Split(os.Getenv("ALLOWED_EMAILS"), ",") {
		if email == strings.ToLower(strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

func cloudflarePublicKey(ctx context.Context, issuer, kid string) (*rsa.PublicKey, error) {
	if strings.TrimSpace(kid) == "" {
		return nil, errors.New("Cloudflare Access token is missing kid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return nil, err
	}
	resp, err := cloudflareHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch Cloudflare Access keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch Cloudflare Access keys: status %d", resp.StatusCode)
	}
	var set cloudflareJWKSet
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode Cloudflare Access keys: %w", err)
	}
	for _, key := range set.Keys {
		if key.Kid != kid || key.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
		if err != nil {
			return nil, fmt.Errorf("decode Cloudflare Access modulus: %w", err)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil {
			return nil, fmt.Errorf("decode Cloudflare Access exponent: %w", err)
		}
		exponent := 0
		for _, b := range eBytes {
			exponent = exponent<<8 + int(b)
		}
		if exponent <= 0 {
			return nil, errors.New("invalid Cloudflare Access exponent")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: exponent}, nil
	}
	return nil, errors.New("Cloudflare Access signing key not found")
}

func verifyCloudflareAccess(ctx context.Context, assertion string) (string, error) {
	issuer, err := cloudflareIssuer()
	if err != nil {
		return "", err
	}
	claims := &cloudflareAccessClaims{}
	token, err := jwt.ParseWithClaims(assertion, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("unexpected Cloudflare Access signing method %q", token.Method.Alg())
		}
		kid, _ := token.Header["kid"].(string)
		return cloudflarePublicKey(ctx, issuer, kid)
	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}), jwt.WithIssuer(issuer), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return "", errors.New("invalid Cloudflare Access assertion")
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" {
		return "", errors.New("Cloudflare Access assertion has no email")
	}
	return email, nil
}

func (h *Handler) CloudflareLogin(w http.ResponseWriter, r *http.Request) {
	assertion := strings.TrimSpace(r.Header.Get("Cf-Access-Jwt-Assertion"))
	if assertion == "" {
		if cookie, err := r.Cookie("CF_Authorization"); err == nil {
			assertion = strings.TrimSpace(cookie.Value)
		}
	}
	if assertion == "" {
		writeError(w, http.StatusUnauthorized, "Cloudflare Access assertion is required")
		return
	}
	email, err := verifyCloudflareAccess(r.Context(), assertion)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid Cloudflare Access session")
		return
	}
	if !cloudflareAllowedEmail(email) {
		writeError(w, http.StatusForbidden, "email is not allowed")
		return
	}
	user, _, err := h.findOrCreateUser(r.Context(), email)
	if err != nil {
		var signupErr SignupError
		if errors.As(err, &signupErr) {
			writeError(w, http.StatusForbidden, signupErr.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}
	tokenString, err := h.issueJWT(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	if err := auth.SetAuthCookies(w, tokenString); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to set auth cookies")
		return
	}
	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(time.Now().Add(auth.AuthTokenTTL())) {
			http.SetCookie(w, cookie)
		}
	}
	writeJSON(w, http.StatusOK, LoginResponse{Token: tokenString, User: userToResponse(user)})

}
