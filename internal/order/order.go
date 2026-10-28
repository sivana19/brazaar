package order

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInsufficientStock = errors.New("insufficient stock for requested item")
	ErrOrderNotFound     = errors.New("order not found")
)

type OrderItem struct {
	ProductID  int `json:"product_id" binding:"required" example:"1"`
	Quantity   int `json:"quantity" binding:"required,gt=0" example:"2"`
	PriceCents int `json:"price_cents" example:"19900"`
}

type Order struct {
	ID         int         `json:"id" example:"101"`
	TotalCents int         `json:"total_cents" example:"39800"`
	Status     string      `json:"status" example:"completed"`
	CreatedAt  time.Time   `json:"created_at" example:"2026-10-28T12:00:00Z"`
	Items      []OrderItem `json:"items"`
}

type CreateOrderInput struct {
	Items []OrderItem `json:"items" binding:"required,dive"`
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateOrder(ctx context.Context, input CreateOrderInput) (*Order, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	totalCents := 0
	finalItems := make([]OrderItem, len(input.Items))

	for i, item := range input.Items {
		var currentStock int
		var priceCents int

		// Lock row for update to prevent concurrent race conditions
		query := `SELECT stock, price_cents FROM products WHERE id = $1 FOR UPDATE;`
		if err := tx.QueryRow(ctx, query, item.ProductID).Scan(&currentStock, &priceCents); err != nil {
			return nil, fmt.Errorf("product %d not found: %w", item.ProductID, err)
		}

		if currentStock < item.Quantity {
			return nil, fmt.Errorf("%w: product %d has %d available, requested %d",
				ErrInsufficientStock, item.ProductID, currentStock, item.Quantity)
		}

		updateStockQuery := `UPDATE products SET stock = stock - $1 WHERE id = $2;`
		if _, err := tx.Exec(ctx, updateStockQuery, item.Quantity, item.ProductID); err != nil {
			return nil, fmt.Errorf("failed to deduct inventory: %w", err)
		}

		finalItems[i] = OrderItem{
			ProductID:  item.ProductID,
			Quantity:   item.Quantity,
			PriceCents: priceCents,
		}
		totalCents += priceCents * item.Quantity
	}

	var orderID int
	var createdAt time.Time
	insertOrderQuery := `
		INSERT INTO orders (total_cents, status) 
		VALUES ($1, 'completed') 
		RETURNING id, created_at;
	`
	if err := tx.QueryRow(ctx, insertOrderQuery, totalCents).Scan(&orderID, &createdAt); err != nil {
		return nil, fmt.Errorf("failed to record order: %w", err)
	}

	for _, item := range finalItems {
		insertItemQuery := `
			INSERT INTO order_items (order_id, product_id, quantity, price_cents)
			VALUES ($1, $2, $3, $4);
		`
		if _, err := tx.Exec(ctx, insertItemQuery, orderID, item.ProductID, item.Quantity, item.PriceCents); err != nil {
			return nil, fmt.Errorf("failed to insert order items: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit order transaction: %w", err)
	}

	return &Order{
		ID:         orderID,
		TotalCents: totalCents,
		Status:     "completed",
		CreatedAt:  createdAt,
		Items:      finalItems,
	}, nil
}

func (r *Repository) GetByID(ctx context.Context, id int) (*Order, error) {
	orderQuery := `SELECT id, total_cents, status, created_at FROM orders WHERE id = $1;`
	var ord Order
	err := r.db.QueryRow(ctx, orderQuery, id).Scan(&ord.ID, &ord.TotalCents, &ord.Status, &ord.CreatedAt)
	if err != nil {
		return nil, ErrOrderNotFound
	}

	itemsQuery := `SELECT product_id, quantity, price_cents FROM order_items WHERE order_id = $1;`
	rows, err := r.db.Query(ctx, itemsQuery, id)
	if err != nil {
		return nil, fmt.Errorf("failed to query order items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item OrderItem
		if err := rows.Scan(&item.ProductID, &item.Quantity, &item.PriceCents); err != nil {
			return nil, fmt.Errorf("failed to scan item: %w", err)
		}
		ord.Items = append(ord.Items, item)
	}

	return &ord, nil
}
