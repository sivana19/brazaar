package order

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler manages HTTP endpoints for orders.
type Handler struct {
	db *pgxpool.Pool
}

func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

// OrderItemInput represents a single product request in an order.
type OrderItemInput struct {
	ProductID int64 `json:"product_id" binding:"required,gt=0"`
	Quantity  int   `json:"quantity" binding:"required,gt=0"`
}

// CreateOrderRequest represents the incoming JSON payload.
type CreateOrderRequest struct {
	Items []OrderItemInput `json:"items" binding:"required,min=1,dive"`
}

// OrderItemResponse represents an item inside the created order.
type OrderItemResponse struct {
	ID         int64 `json:"id"`
	ProductID  int64 `json:"product_id"`
	Quantity   int   `json:"quantity"`
	PriceCents int64 `json:"price_cents"`
}

// OrderResponse represents the final returned order object.
type OrderResponse struct {
	ID         int64               `json:"id"`
	UserID     int64               `json:"user_id"`
	TotalCents int64               `json:"total_cents"`
	Status     string              `json:"status"`
	CreatedAt  time.Time           `json:"created_at"`
	Items      []OrderItemResponse `json:"items"`
}

// extractUserID safely extracts and converts the user ID from the Gin context.
func extractUserID(c *gin.Context) (int64, error) {
	val, exists := c.Get("user_id")
	if !exists {
		val, exists = c.Get("userID")
	}
	if !exists {
		return 0, errors.New("user ID missing from session context")
	}

	switch v := val.(type) {
	case float64:
		return int64(v), nil
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid string user ID: %w", err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported user ID format: %T", val)
	}
}

// CreateOrder handles creating an order and deducting inventory atomically.
func (h *Handler) CreateOrder(c *gin.Context) {
	userID, err := extractUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// 1. Begin atomic transaction
	tx, err := h.db.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initiate transaction"})
		return
	}
	defer tx.Rollback(ctx)

	var totalCents int64 = 0
	type processedItem struct {
		productID  int64
		quantity   int
		priceCents int64
	}
	itemsToInsert := make([]processedItem, 0, len(req.Items))

	// 2. Lock each product row, check stock, calculate totals, and decrement stock
	for _, item := range req.Items {
		var (
			currentStock int
			priceCents   int64
		)

		// Row-level lock prevents race conditions on inventory
		query := `SELECT stock, price_cents FROM products WHERE id = $1 FOR UPDATE`
		err := tx.QueryRow(ctx, query, item.ProductID).Scan(&currentStock, &priceCents)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{
					"error": fmt.Sprintf("product with ID %d does not exist", item.ProductID),
				})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query product details"})
			return
		}

		if currentStock < item.Quantity {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("insufficient stock for product ID %d (available: %d, requested: %d)",
					item.ProductID, currentStock, item.Quantity),
			})
			return
		}

		// Deduct stock
		updateQuery := `UPDATE products SET stock = stock - $1, updated_at = NOW() WHERE id = $2`
		_, err = tx.Exec(ctx, updateQuery, item.Quantity, item.ProductID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to deduct product inventory"})
			return
		}

		totalCents += priceCents * int64(item.Quantity)
		itemsToInsert = append(itemsToInsert, processedItem{
			productID:  item.ProductID,
			quantity:   item.Quantity,
			priceCents: priceCents,
		})
	}

	// 3. Insert order record
	var orderID int64
	var createdAt time.Time
	orderQuery := `
		INSERT INTO orders (user_id, total_cents, status, created_at)
		VALUES ($1, $2, 'pending', NOW())
		RETURNING id, created_at
	`
	err = tx.QueryRow(ctx, orderQuery, userID, totalCents).Scan(&orderID, &createdAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order entry"})
		return
	}

	// 4. Insert order items
	responseItems := make([]OrderItemResponse, 0, len(itemsToInsert))
	for _, it := range itemsToInsert {
		var itemID int64
		itemInsertQuery := `
			INSERT INTO order_items (order_id, product_id, quantity, price_cents, created_at)
			VALUES ($1, $2, $3, $4, NOW())
			RETURNING id
		`
		err = tx.QueryRow(ctx, itemInsertQuery, orderID, it.productID, it.quantity, it.priceCents).Scan(&itemID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist order items"})
			return
		}

		responseItems = append(responseItems, OrderItemResponse{
			ID:         itemID,
			ProductID:  it.productID,
			Quantity:   it.quantity,
			PriceCents: it.priceCents,
		})
	}

	// 5. Commit transaction
	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit order transaction"})
		return
	}

	c.JSON(http.StatusCreated, OrderResponse{
		ID:         orderID,
		UserID:     userID,
		TotalCents: totalCents,
		Status:     "pending",
		CreatedAt:  createdAt,
		Items:      responseItems,
	})
}

// GetOrder fetches a single order by ID.
func (h *Handler) GetOrder(c *gin.Context) {
	orderIDStr := c.Param("id")
	orderID, err := strconv.ParseInt(orderIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order ID parameter"})
		return
	}

	ctx := c.Request.Context()

	var resp OrderResponse
	orderQuery := `SELECT id, user_id, total_cents, status, created_at FROM orders WHERE id = $1`
	err = h.db.QueryRow(ctx, orderQuery, orderID).Scan(
		&resp.ID,
		&resp.UserID,
		&resp.TotalCents,
		&resp.Status,
		&resp.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error querying order"})
		return
	}

	itemQuery := `SELECT id, product_id, quantity, price_cents FROM order_items WHERE order_id = $1`
	rows, err := h.db.Query(ctx, itemQuery, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve order items"})
		return
	}
	defer rows.Close()

	resp.Items = make([]OrderItemResponse, 0)
	for rows.Next() {
		var it OrderItemResponse
		if err := rows.Scan(&it.ID, &it.ProductID, &it.Quantity, &it.PriceCents); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed scanning item records"})
			return
		}
		resp.Items = append(resp.Items, it)
	}

	c.JSON(http.StatusOK, resp)
}
