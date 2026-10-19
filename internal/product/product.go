package product

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Product represents an item stored in PostgreSQL.
type Product struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	PriceCents  int       `json:"price_cents"`
	Stock       int       `json:"stock"`
	CreatedAt   time.Time `json:"created_at"`
}

// CreateProductInput defines the expected JSON payload when creating a product.
type CreateProductInput struct {
	Name        string `json:"name" binding:"required,min=2,max=255"`
	Description string `json:"description"`
	PriceCents  int    `json:"price_cents" binding:"required,gte=0"`
	Stock       int    `json:"stock" binding:"gte=0"`
}

// Repository encapsulates database queries for products.
type Repository struct {
	db *pgxpool.Pool
}

// NewRepository initializes a new product repository with the database pool.
func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// GetAll retrieves all products ordered by ID.
func (r *Repository) GetAll(ctx context.Context) ([]Product, error) {
	query := `
		SELECT id, name, description, price_cents, stock, created_at 
		FROM products 
		ORDER BY id ASC;
	`

	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query products: %w", err)
	}
	defer rows.Close()

	products := make([]Product, 0)
	for rows.Next() {
		var p Product
		err := rows.Scan(
			&p.ID,
			&p.Name,
			&p.Description,
			&p.PriceCents,
			&p.Stock,
			&p.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		products = append(products, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return products, nil
}

// GetByID retrieves a single product by its ID. Returns (nil, nil) if not found.
func (r *Repository) GetByID(ctx context.Context, id int) (*Product, error) {
	query := `
		SELECT id, name, description, price_cents, stock, created_at
		FROM products
		WHERE id = $1;
	`

	var p Product
	err := r.db.QueryRow(ctx, query, id).Scan(
		&p.ID,
		&p.Name,
		&p.Description,
		&p.PriceCents,
		&p.Stock,
		&p.CreatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get product by id: %w", err)
	}

	return &p, nil
}

// Create inserts a new product and returns the created record.
func (r *Repository) Create(ctx context.Context, input CreateProductInput) (*Product, error) {
	query := `
		INSERT INTO products (name, description, price_cents, stock)
		VALUES ($1, $2, $3, $4)
		RETURNING id, name, description, price_cents, stock, created_at;
	`

	var p Product
	err := r.db.QueryRow(
		ctx,
		query,
		input.Name,
		input.Description,
		input.PriceCents,
		input.Stock,
	).Scan(
		&p.ID,
		&p.Name,
		&p.Description,
		&p.PriceCents,
		&p.Stock,
		&p.CreatedAt,
	)

	if err != nil {
		return nil, fmt.Errorf("create product: %w", err)
	}

	return &p, nil
}
