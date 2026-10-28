package product

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrProductNotFound = errors.New("product not found")
	ErrEmptyName       = errors.New("product name cannot be empty")
	ErrInvalidPrice    = errors.New("price must be greater than zero")
	ErrNegativeStock   = errors.New("stock cannot be negative")
)

type Product struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	PriceCents  int       `json:"price_cents"`
	Stock       int       `json:"stock"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateProductInput struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	PriceCents  int    `json:"price_cents" binding:"required"`
	Stock       int    `json:"stock"`
}

func (in CreateProductInput) Validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return ErrEmptyName
	}
	if in.PriceCents <= 0 {
		return ErrInvalidPrice
	}
	if in.Stock < 0 {
		return ErrNegativeStock
	}
	return nil
}

type UpdateProductInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	PriceCents  *int    `json:"price_cents"`
	Stock       *int    `json:"stock"`
}

type ListProductsFilter struct {
	Search   string `form:"search"`
	MinPrice int    `form:"min_price"`
	MaxPrice int    `form:"max_price"`
	Limit    int    `form:"limit"`
	Offset   int    `form:"offset"`
}

type PaginatedProductsResponse struct {
	Data       []Product `json:"data"`
	TotalCount int       `json:"total_count"`
	Limit      int       `json:"limit"`
	Offset     int       `json:"offset"`
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, input CreateProductInput) (*Product, error) {
	query := `
		INSERT INTO products (name, description, price_cents, stock)
		VALUES ($1, $2, $3, $4)
		RETURNING id, name, description, price_cents, stock, created_at, updated_at;
	`

	var p Product
	err := r.db.QueryRow(ctx, query, input.Name, input.Description, input.PriceCents, input.Stock).Scan(
		&p.ID,
		&p.Name,
		&p.Description,
		&p.PriceCents,
		&p.Stock,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert product: %w", err)
	}

	return &p, nil
}

func (r *Repository) List(ctx context.Context, filter ListProductsFilter) (*PaginatedProductsResponse, error) {
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 20
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	baseQuery := "FROM products WHERE 1=1"
	args := []any{}
	argIdx := 1

	if strings.TrimSpace(filter.Search) != "" {
		baseQuery += fmt.Sprintf(" AND (name ILIKE $%d OR description ILIKE $%d)", argIdx, argIdx)
		args = append(args, "%"+strings.TrimSpace(filter.Search)+"%")
		argIdx++
	}

	if filter.MinPrice > 0 {
		baseQuery += fmt.Sprintf(" AND price_cents >= $%d", argIdx)
		args = append(args, filter.MinPrice)
		argIdx++
	}

	if filter.MaxPrice > 0 {
		baseQuery += fmt.Sprintf(" AND price_cents <= $%d", argIdx)
		args = append(args, filter.MaxPrice)
		argIdx++
	}

	// 1. Total matching count
	countQuery := "SELECT COUNT(*) " + baseQuery
	var totalCount int
	if err := r.db.QueryRow(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("failed to count products: %w", err)
	}

	// 2. Fetch paginated records using standard single-line string formatting
	dataQuery := fmt.Sprintf("SELECT id, name, description, price_cents, stock, created_at, updated_at %s ORDER BY id DESC LIMIT $%d OFFSET $%d", baseQuery, argIdx, argIdx+1)

	args = append(args, filter.Limit, filter.Offset)

	rows, err := r.db.Query(ctx, dataQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query products: %w", err)
	}
	defer rows.Close()

	items := make([]Product, 0)
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.Stock, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan product: %w", err)
		}
		items = append(items, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return &PaginatedProductsResponse{
		Data:       items,
		TotalCount: totalCount,
		Limit:      filter.Limit,
		Offset:     filter.Offset,
	}, nil
}

func (r *Repository) GetAll(ctx context.Context) ([]Product, error) {
	query := `SELECT id, name, description, price_cents, stock, created_at, updated_at FROM products ORDER BY id ASC;`

	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query products: %w", err)
	}
	defer rows.Close()

	products := make([]Product, 0)
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.Stock, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan product row: %w", err)
		}
		products = append(products, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return products, nil
}

func (r *Repository) GetByID(ctx context.Context, id int) (*Product, error) {
	query := `SELECT id, name, description, price_cents, stock, created_at, updated_at FROM products WHERE id = $1;`

	var p Product
	err := r.db.QueryRow(ctx, query, id).Scan(
		&p.ID,
		&p.Name,
		&p.Description,
		&p.PriceCents,
		&p.Stock,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProductNotFound
	} else if err != nil {
		return nil, fmt.Errorf("failed to get product: %w", err)
	}

	return &p, nil
}

func (r *Repository) Update(ctx context.Context, id int, input UpdateProductInput) (*Product, error) {
	existing, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if input.Name != nil {
		existing.Name = *input.Name
	}
	if input.Description != nil {
		existing.Description = *input.Description
	}
	if input.PriceCents != nil {
		existing.PriceCents = *input.PriceCents
	}
	if input.Stock != nil {
		existing.Stock = *input.Stock
	}

	query := `
		UPDATE products
		SET name = $1, description = $2, price_cents = $3, stock = $4, updated_at = NOW()
		WHERE id = $5
		RETURNING id, name, description, price_cents, stock, created_at, updated_at;
	`

	var p Product
	err = r.db.QueryRow(ctx, query, existing.Name, existing.Description, existing.PriceCents, existing.Stock, id).Scan(
		&p.ID,
		&p.Name,
		&p.Description,
		&p.PriceCents,
		&p.Stock,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update product: %w", err)
	}

	return &p, nil
}

func (r *Repository) Delete(ctx context.Context, id int) (bool, error) {
	query := `DELETE FROM products WHERE id = $1;`

	commandTag, err := r.db.Exec(ctx, query, id)
	if err != nil {
		return false, fmt.Errorf("failed to delete product: %w", err)
	}

	if commandTag.RowsAffected() == 0 {
		return false, ErrProductNotFound
	}

	return true, nil
}
