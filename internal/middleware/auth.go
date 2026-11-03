package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// RequireAuth validates incoming JWT tokens and injects claims into Gin context.
func RequireAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authorization header is required"})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authorization header format must be Bearer <token>"})
			return
		}

		tokenString := parts[1]

		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return []byte(secret), nil
		})

		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unable to parse token claims"})
			return
		}

		// Inject user identity across common claim keys
		if sub, exists := claims["sub"]; exists {
			c.Set("userID", sub)
			c.Set("user_id", sub)
		} else if id, exists := claims["id"]; exists {
			c.Set("userID", id)
			c.Set("user_id", id)
		} else if uid, exists := claims["user_id"]; exists {
			c.Set("userID", uid)
			c.Set("user_id", uid)
		}

		if email, exists := claims["email"]; exists {
			c.Set("userEmail", email)
		}

		if role, exists := claims["role"]; exists {
			c.Set("userRole", role)
		}

		c.Next()
	}
}

// AuthMiddleware is an alias for RequireAuth for backward compatibility.
func AuthMiddleware(secret string) gin.HandlerFunc {
	return RequireAuth(secret)
}
