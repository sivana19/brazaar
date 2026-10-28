package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrUserNotFound       = errors.New("user not found")
)

type User struct {
	ID        int       `json:"id" example:"1"`
	Email     string    `json:"email" example:"customer@bazaar.com"`
	Role      string    `json:"role" example:"customer"`
	CreatedAt time.Time `json:"created_at" example:"2026-10-28T12:00:00Z"`
}

type RegisterInput struct {
	Email    string `json:"email" binding:"required,email" example:"user@example.com"`
	Password string `json:"password" binding:"required,min=8" example:"supersecretpassword"`
}

type LoginInput struct {
	Email    string `json:"email" binding:"required,email" example:"user@example.com"`
	Password string `json:"password" binding:"required" example:"supersecretpassword"`
}

type Repository struct {
	db        *pgxpool.Pool
	jwtSecret []byte
}

func NewRepository(db *pgxpool.Pool, jwtSecret string) *Repository {
	return &Repository{
		db:        db,
		jwtSecret: []byte(jwtSecret),
	}
}

func (r *Repository) Register(ctx context.Context, input RegisterInput) (*User, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	query := `
		INSERT INTO users (email, password_hash, role)
		VALUES ($1, $2, 'customer')
		RETURNING id, email, role, created_at;
	`

	var u User
	err = r.db.QueryRow(ctx, query, input.Email, string(hashedPassword)).Scan(
		&u.ID,
		&u.Email,
		&u.Role,
		&u.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to register user: %w", err)
	}

	return &u, nil
}

func (r *Repository) Login(ctx context.Context, input LoginInput) (string, *User, error) {
	query := `SELECT id, email, password_hash, role, created_at FROM users WHERE email = $1;`

	var u User
	var passwordHash string
	err := r.db.QueryRow(ctx, query, input.Email).Scan(
		&u.ID,
		&u.Email,
		&passwordHash,
		&u.Role,
		&u.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrInvalidCredentials
	} else if err != nil {
		return "", nil, fmt.Errorf("login failed: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)); err != nil {
		return "", nil, ErrInvalidCredentials
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  u.ID,
		"role": u.Role,
		"exp":  time.Now().Add(24 * time.Hour).Unix(),
	})

	tokenString, err := token.SignedString(r.jwtSecret)
	if err != nil {
		return "", nil, fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, &u, nil
}
