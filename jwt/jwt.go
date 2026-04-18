package jwt

import (
	"fmt"
	"time"

	"github.com/aidenappl/lattice-api/env"
	jwtlib "github.com/golang-jwt/jwt/v5"
)

const (
	issuer             = "lattice"
	accessTokenExpiry  = 15 * time.Minute
	refreshTokenExpiry = 7 * 24 * time.Hour
)

type Claims struct {
	jwtlib.RegisteredClaims
	UserID int    `json:"user_id"`
	Type   string `json:"type"` // "access" or "refresh"
}

func NewAccessToken(userID int) (string, time.Time, error) {
	expiresAt := time.Now().Add(accessTokenExpiry)
	claims := Claims{
		RegisteredClaims: jwtlib.RegisteredClaims{
			Issuer:    issuer,
			ExpiresAt: jwtlib.NewNumericDate(expiresAt),
			IssuedAt:  jwtlib.NewNumericDate(time.Now()),
		},
		UserID: userID,
		Type:   "access",
	}

	token := jwtlib.NewWithClaims(jwtlib.SigningMethodHS512, claims)
	signed, err := token.SignedString([]byte(env.JWTSigningKey))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to sign access token: %w", err)
	}
	return signed, expiresAt, nil
}

func NewRefreshToken(userID int) (string, time.Time, error) {
	expiresAt := time.Now().Add(refreshTokenExpiry)
	claims := Claims{
		RegisteredClaims: jwtlib.RegisteredClaims{
			Issuer:    issuer,
			ExpiresAt: jwtlib.NewNumericDate(expiresAt),
			IssuedAt:  jwtlib.NewNumericDate(time.Now()),
		},
		UserID: userID,
		Type:   "refresh",
	}

	token := jwtlib.NewWithClaims(jwtlib.SigningMethodHS512, claims)
	signed, err := token.SignedString([]byte(env.JWTSigningKey))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to sign refresh token: %w", err)
	}
	return signed, expiresAt, nil
}

func ValidateToken(tokenStr string) (*Claims, error) {
	token, err := jwtlib.ParseWithClaims(tokenStr, &Claims{}, func(t *jwtlib.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwtlib.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(env.JWTSigningKey), nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	if claims.Issuer != issuer {
		return nil, fmt.Errorf("invalid token issuer: %s", claims.Issuer)
	}

	return claims, nil
}

func ValidateAccessToken(tokenStr string) (int, error) {
	claims, err := ValidateToken(tokenStr)
	if err != nil {
		return 0, err
	}
	if claims.Type != "access" {
		return 0, fmt.Errorf("expected access token, got %s", claims.Type)
	}
	return claims.UserID, nil
}

func ValidateRefreshToken(tokenStr string) (int, error) {
	claims, err := ValidateToken(tokenStr)
	if err != nil {
		return 0, err
	}
	if claims.Type != "refresh" {
		return 0, fmt.Errorf("expected refresh token, got %s", claims.Type)
	}
	return claims.UserID, nil
}
