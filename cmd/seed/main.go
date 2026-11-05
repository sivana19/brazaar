package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type SeedProduct struct {
	Name        string
	Description string
	PriceCents  int64
	Stock       int
}

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("unable to connect to database: %v", err)
	}
	defer pool.Close()

	fmt.Println("[*] Connected to Neon PostgreSQL database. Seeding data...")

	testEmail := "tester@bazaar.local"
	plainPassword := "SecurePassword123!"

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(plainPassword), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("failed to hash test password: %v", err)
	}

	// 1. Inspect existing columns in the 'users' table
	rows, err := pool.Query(ctx, `
		SELECT column_name 
		FROM information_schema.columns 
		WHERE table_name = 'users';
	`)
	if err != nil {
		log.Fatalf("failed to read table schema: %v", err)
	}
	defer rows.Close()

	userColumns := make(map[string]bool)
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err == nil {
			userColumns[col] = true
		}
	}

	// 2. Build insert statement based on present columns
	var userID int64
	var userQuery string

	pwCol := "password_hash"
	if !userColumns["password_hash"] && userColumns["password"] {
		pwCol = "password"
	}

	if userColumns["full_name"] {
		userQuery = fmt.Sprintf(`
			INSERT INTO users (email, %s, full_name)
			VALUES ($1, $2, 'Tester Account')
			ON CONFLICT (email) DO UPDATE SET %s = EXCLUDED.%s
			RETURNING id;
		`, pwCol, pwCol, pwCol)
	} else if userColumns["name"] {
		userQuery = fmt.Sprintf(`
			INSERT INTO users (email, %s, name)
			VALUES ($1, $2, 'Tester Account')
			ON CONFLICT (email) DO UPDATE SET %s = EXCLUDED.%s
			RETURNING id;
		`, pwCol, pwCol, pwCol)
	} else {
		userQuery = fmt.Sprintf(`
			INSERT INTO users (email, %s)
			VALUES ($1, $2)
			ON CONFLICT (email) DO UPDATE SET %s = EXCLUDED.%s
			RETURNING id;
		`, pwCol, pwCol, pwCol)
	}

	err = pool.QueryRow(ctx, userQuery, testEmail, string(hashedPassword)).Scan(&userID)
	if err != nil {
		log.Fatalf("failed to seed user: %v", err)
	}
	fmt.Printf("[✓] User seeded/verified -> ID: %d | Email: %s | Password: %s\n", userID, testEmail, plainPassword)

	// 3. Seed Products
	products := []SeedProduct{
		{
			Name:        "Ergonomic Mechanical Keyboard",
			Description: "Hot-swappable switches with tactile feedback",
			PriceCents:  11999,
			Stock:       50,
		},
		{
			Name:        "Wireless Precision Mouse",
			Description: "Ultra-low latency dual-mode 2.4GHz",
			PriceCents:  6499,
			Stock:       30,
		},
		{
			Name:        "Desk Mat Extended",
			Description: "Water-resistant micro-woven cloth surface",
			PriceCents:  2999,
			Stock:       100,
		},
	}

	for _, p := range products {
		var productID int64
		var currentStock int

		query := `
			INSERT INTO products (name, description, price_cents, stock)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (name) DO UPDATE 
			SET stock = EXCLUDED.stock, price_cents = EXCLUDED.price_cents
			RETURNING id, stock;
		`
		err := pool.QueryRow(ctx, query, p.Name, p.Description, p.PriceCents, p.Stock).Scan(&productID, &currentStock)
		if err != nil {
			fallback := `
				INSERT INTO products (name, description, price_cents, stock)
				VALUES ($1, $2, $3, $4)
				RETURNING id, stock;
			`
			if fbErr := pool.QueryRow(ctx, fallback, p.Name, p.Description, p.PriceCents, p.Stock).Scan(&productID, &currentStock); fbErr != nil {
				log.Fatalf("failed to insert product %s: %v", p.Name, fbErr)
			}
		}

		fmt.Printf("[✓] Product seeded -> ID: %d | %-32s | Stock: %-3d | Price: $%.2f\n",
			productID, p.Name, currentStock, float64(p.PriceCents)/100.0)
	}

	fmt.Println("\n[✓] Database seeding completed successfully.")
}
