package order

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrProductNotFound   = errors.New("product not found")
	ErrInsufficientStock = errors.New("insufficient stock for requested product")
	ErrEmptyOrder        = errors.New("order must contain at least one item")
)

type OrderItemInput struct {
	ProductID int `json:"product_id" binding:"required,gt=0"`
	Quantity  int `json:"quantity" binding:"required,gt=0"`
}

type CreateOrderInput struct {
	CustomerEmail string           `json:"customer_email" binding:"required,email"`
	Items         []OrderItemInput `json:"items" binding:"required,dive"`
}

type OrderItem struct {
	ID             int `json:"id"`
	ProductID      int `json:"product_id"`
	Quantity       int `json:"quantity"`
	UnitPriceCents int `json:"unit_price_cents"`
}

type Order struct {
	ID            int         `json:"id"`
	CustomerEmail string      `json:"customer_email"`
	TotalCents    int         `json:"total_cents"`
	Status        string      `json:"status"`
	CreatedAt     time.Time   `json:"created_at"`
	Items         []OrderItem `json:"items"`
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// CreateOrder executes an atomic transaction with row-level locking (FOR UPDATE)
func (r *Repository) CreateOrder(ctx context.Context, input CreateOrderInput) (*Order, error) {
	if len(input.Items) == 0 {
		return nil, ErrEmptyOrder
	}

	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var totalCents int
	orderItems := make([]OrderItem, 0, len(input.Items))

	for _, item := range input.Items {
		var currentStock int
		var unitPriceCents int

		queryLock := `
			SELECT stock, price_cents 
			FROM products 
			WHERE id = $1 
			FOR UPDATE;
		`
		err := tx.QueryRow(ctx, queryLock, item.ProductID).Scan(&currentStock, &unitPriceCents)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("%w: ID %d", ErrProductNotFound, item.ProductID)
			}
			return nil, fmt.Errorf("lock product row: %w", err)
		}

		if currentStock < item.Quantity {
			return nil, fmt.Errorf("%w: product ID %d has %d in stock, requested %d",
				ErrInsufficientStock, item.ProductID, currentStock, item.Quantity)
		}

		queryUpdateStock := `
			UPDATE products 
			SET stock = stock - $1 
			WHERE id = $2;
		`
		if _, err := tx.Exec(ctx, queryUpdateStock, item.Quantity, item.ProductID); err != nil {
			return nil, fmt.Errorf("deduct stock: %w", err)
		}

		itemTotal := unitPriceCents * item.Quantity
		totalCents += itemTotal

		orderItems = append(orderItems, OrderItem{
			ProductID:      item.ProductID,
			Quantity:       item.Quantity,
			UnitPriceCents: unitPriceCents,
		})
	}

	var createdOrder Order
	queryOrder := `
		INSERT INTO orders (customer_email, total_cents, status) 
		VALUES ($1, $2, 'completed') 
		RETURNING id, customer_email, total_cents, status, created_at;
	`
	err = tx.QueryRow(ctx, queryOrder, input.CustomerEmail, totalCents).Scan(
		&createdOrder.ID,
		&createdOrder.CustomerEmail,
		&createdOrder.TotalCents,
		&createdOrder.Status,
		&createdOrder.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert order: %w", err)
	}

	for i := range orderItems {
		queryItem := `
			INSERT INTO order_items (order_id, product_id, quantity, unit_price_cents) 
			VALUES ($1, $2, $3, $4) 
			RETURNING id;
		`
		err := tx.QueryRow(
			ctx,
			queryItem,
			createdOrder.ID,
			orderItems[i].ProductID,
			orderItems[i].Quantity,
			orderItems[i].UnitPriceCents,
		).Scan(&orderItems[i].ID)

		if err != nil {
			return nil, fmt.Errorf("insert order item: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit order transaction: %w", err)
	}

	createdOrder.Items = orderItems
	return &createdOrder, nil
}

// GetByID retrieves an order and its associated line items by order ID.
func (r *Repository) GetByID(ctx context.Context, id int) (*Order, error) {
	queryOrder := `
		SELECT id, customer_email, total_cents, status, created_at
		FROM orders
		WHERE id = $1;
	`

	var o Order
	err := r.db.QueryRow(ctx, queryOrder, id).Scan(
		&o.ID,
		&o.CustomerEmail,
		&o.TotalCents,
		&o.Status,
		&o.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query order: %w", err)
	}

	queryItems := `
		SELECT id, product_id, quantity, unit_price_cents
		FROM order_items
		WHERE order_id = $1
		ORDER BY id ASC;
	`

	rows, err := r.db.Query(ctx, queryItems, id)
	if err != nil {
		return nil, fmt.Errorf("query order items: %w", err)
	}
	defer rows.Close()

	o.Items = make([]OrderItem, 0)
	for rows.Next() {
		var item OrderItem
		if err := rows.Scan(&item.ID, &item.ProductID, &item.Quantity, &item.UnitPriceCents); err != nil {
			return nil, fmt.Errorf("scan order item: %w", err)
		}
		o.Items = append(o.Items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("order items rows iteration: %w", err)
	}

	return &o, nil
}
