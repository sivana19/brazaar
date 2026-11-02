package order

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- Domain Models & DTOs ---

type OrderItemRequest struct {
	ProductID int `json:"product_id" binding:"required,gt=0"`
	Quantity  int `json:"quantity" binding:"required,gt=0"`
}

type CreateOrderRequest struct {
	Items []OrderItemRequest `json:"items" binding:"required,dive,min=1"`
}

type OrderItem struct {
	ID         int       `json:"id"`
	OrderID    int       `json:"order_id"`
	ProductID  int       `json:"product_id"`
	Quantity   int       `json:"quantity"`
	PriceCents int       `json:"price_cents"`
	CreatedAt  time.Time `json:"created_at"`
}

type Order struct {
	ID         int         `json:"id"`
	TotalCents int         `json:"total_cents"`
	Status     string      `json:"status"`
	Items      []OrderItem `json:"items,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
}

// --- Handler ---

type Handler struct {
	pool *pgxpool.Pool
}

func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

// CreateOrder handles POST /orders
// @Summary      Create an order
// @Description  Create an order with inventory validation and concurrency row locking
// @Tags         orders
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request body CreateOrderRequest true "Order Items"
// @Success      201  {object} map[string]interface{}
// @Failure      400  {object} map[string]interface{}
// @Failure      401  {object} map[string]interface{}
// @Failure      404  {object} map[string]interface{}
// @Failure      500  {object} map[string]interface{}
// @Router       /orders [post]
func (h *Handler) CreateOrder(c *gin.Context) {
	val, exists := c.Get("userID")
	if !exists {
		log.Println("[ORDER] unauthorized: missing userID context")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing user context"})
		return
	}

	var userID int
	switch v := val.(type) {
	case int:
		userID = v
	case float64:
		userID = int(v)
	case int64:
		userID = int(v)
	case string:
		parsed, err := strconv.Atoi(v)
		if err != nil {
			log.Printf("[ORDER] invalid user id format (%s): %v", v, err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid user id in token"})
			return
		}
		userID = parsed
	default:
		log.Printf("[ORDER] unexpected userID type: %T", val)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid user authentication payload"})
		return
	}
	_ = userID // Available for schema upgrades when user_id column is added to orders

	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[ORDER] validation error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	order, err := h.createOrderTx(c.Request.Context(), req)
	if err != nil {
		log.Printf(">>> CREATE ORDER ERROR: %v", err)
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "one or more products do not exist"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Printf("[ORDER] Successfully placed Order ID #%d (total %d cents)", order.ID, order.TotalCents)
	c.JSON(http.StatusCreated, gin.H{
		"message": "order created successfully",
		"data":    order,
	})
}

// GetOrder handles GET /orders/:id
// @Summary      Get order details
// @Description  Get order details and items by order ID
// @Tags         orders
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Order ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      401  {object}  map[string]interface{}
// @Failure      404  {object}  map[string]interface{}
// @Failure      500  {object}  map[string]interface{}
// @Router       /orders/{id} [get]
func (h *Handler) GetOrder(c *gin.Context) {
	orderID := c.Param("id")

	query := `
		SELECT id, total_cents, status, created_at
		FROM orders
		WHERE id = $1
	`

	var o Order
	err := h.pool.QueryRow(c.Request.Context(), query, orderID).Scan(
		&o.ID,
		&o.TotalCents,
		&o.Status,
		&o.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		log.Printf("[ORDER] query error for order %s: %v", orderID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query order"})
		return
	}

	itemsQuery := `
		SELECT id, order_id, product_id, quantity, price_cents, created_at
		FROM order_items
		WHERE order_id = $1
	`
	rows, err := h.pool.Query(c.Request.Context(), itemsQuery, o.ID)
	if err != nil {
		log.Printf("[ORDER] query items error for order %d: %v", o.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve order items"})
		return
	}
	defer rows.Close()

	for rows.Next() {
		var item OrderItem
		if err := rows.Scan(&item.ID, &item.OrderID, &item.ProductID, &item.Quantity, &item.PriceCents, &item.CreatedAt); err != nil {
			log.Printf("[ORDER] row scan error for order item: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to scan order item"})
			return
		}
		o.Items = append(o.Items, item)
	}

	if rows.Err() != nil {
		log.Printf("[ORDER] rows iteration error: %v", rows.Err())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error reading order items"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": o})
}

// --- Transaction Execution ---

func (h *Handler) createOrderTx(ctx context.Context, req CreateOrderRequest) (*Order, error) {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("could not start transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	totalCents := 0
	type itemDetail struct {
		productID  int
		quantity   int
		priceCents int
	}
	var details []itemDetail

	for _, item := range req.Items {
		var availableStock int
		var priceCents int

		// Row-level lock via SELECT FOR UPDATE prevents overselling under concurrent load
		err := tx.QueryRow(ctx, `
			SELECT stock, price_cents
			FROM products
			WHERE id = $1
			FOR UPDATE
		`, item.ProductID).Scan(&availableStock, &priceCents)

		if err != nil {
			return nil, fmt.Errorf("product %d lookup: %w", item.ProductID, err)
		}

		if availableStock < item.Quantity {
			return nil, fmt.Errorf("insufficient stock for product %d: available %d, requested %d", item.ProductID, availableStock, item.Quantity)
		}

		// Deduct inventory
		_, err = tx.Exec(ctx, `
			UPDATE products
			SET stock = stock - $1, updated_at = NOW()
			WHERE id = $2
		`, item.Quantity, item.ProductID)
		if err != nil {
			return nil, fmt.Errorf("failed to deduct stock for product %d: %w", item.ProductID, err)
		}

		totalCents += priceCents * item.Quantity
		details = append(details, itemDetail{
			productID:  item.ProductID,
			quantity:   item.Quantity,
			priceCents: priceCents,
		})
	}

	// Insert order record matching orders table schema
	var order Order
	err = tx.QueryRow(ctx, `
		INSERT INTO orders (total_cents, status, created_at)
		VALUES ($1, 'completed', NOW())
		RETURNING id, total_cents, status, created_at
	`, totalCents).Scan(
		&order.ID,
		&order.TotalCents,
		&order.Status,
		&order.CreatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return nil, fmt.Errorf("orders insert DB error (Code: %s, Message: %s, Detail: %s)", pgErr.Code, pgErr.Message, pgErr.Detail)
		}
		return nil, fmt.Errorf("failed to insert order: %w", err)
	}

	// Batch insert line items matching order_items schema
	batch := &pgx.Batch{}
	for _, d := range details {
		batch.Queue(`
			INSERT INTO order_items (order_id, product_id, quantity, price_cents, created_at)
			VALUES ($1, $2, $3, $4, NOW())
			RETURNING id, order_id, product_id, quantity, price_cents, created_at
		`, order.ID, d.productID, d.quantity, d.priceCents)
	}

	batchResults := tx.SendBatch(ctx, batch)
	for range details {
		var oi OrderItem
		err := batchResults.QueryRow().Scan(
			&oi.ID,
			&oi.OrderID,
			&oi.ProductID,
			&oi.Quantity,
			&oi.PriceCents,
			&oi.CreatedAt,
		)
		if err != nil {
			batchResults.Close()
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				return nil, fmt.Errorf("order_items batch DB error (Code: %s, Message: %s, Detail: %s)", pgErr.Code, pgErr.Message, pgErr.Detail)
			}
			return nil, fmt.Errorf("failed to insert order item: %w", err)
		}
		order.Items = append(order.Items, oi)
	}

	if err := batchResults.Close(); err != nil {
		return nil, fmt.Errorf("failed closing batch inserts: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return &order, nil
}
