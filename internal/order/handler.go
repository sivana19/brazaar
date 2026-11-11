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
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	db *pgxpool.Pool
}

func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

// getUserID extracts the authenticated user ID safely from Gin context
func getUserID(c *gin.Context) (int64, error) {
	val, exists := c.Get("user_id")
	if !exists {
		return 0, errors.New("user not authenticated")
	}

	switch v := val.(type) {
	case int64:
		return v, nil
	case float64:
		return int64(v), nil
	case int:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("invalid user_id format: %T", val)
	}
}

// CreateOrder handles POST /orders
func (h *Handler) CreateOrder(c *gin.Context) {
	userID, err := getUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	// Begin explicit transaction
	tx, err := h.db.Begin(ctx)
	if err != nil {
		log.Printf("[ORDER ERROR] Begin transaction failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start transaction: " + err.Error()})
		return
	}
	defer tx.Rollback(ctx)

	var totalCents int64 = 0
	type itemDetail struct {
		productID  int64
		quantity   int
		priceCents int64
	}
	var details []itemDetail

	// Verify inventory, calculate total, lock row FOR UPDATE to prevent race conditions
	for _, item := range req.Items {
		var currentStock int
		var priceCents int64

		query := "SELECT stock, price_cents FROM products WHERE id = $1 FOR UPDATE"
		err := tx.QueryRow(ctx, query, item.ProductID).Scan(&currentStock, &priceCents)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("product with id %d not found", item.ProductID)})
				return
			}
			log.Printf("[ORDER ERROR] Product fetch error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query product: " + err.Error()})
			return
		}

		if currentStock < item.Quantity {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("insufficient stock for product id %d (available: %d, requested: %d)", item.ProductID, currentStock, item.Quantity),
			})
			return
		}

		// Deduct stock immediately in transaction
		updateStockQuery := "UPDATE products SET stock = stock - $1, updated_at = NOW() WHERE id = $2"
		_, err = tx.Exec(ctx, updateStockQuery, item.Quantity, item.ProductID)
		if err != nil {
			log.Printf("[ORDER ERROR] Failed to deduct stock: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update stock: " + err.Error()})
			return
		}

		totalCents += priceCents * int64(item.Quantity)
		details = append(details, itemDetail{
			productID:  item.ProductID,
			quantity:   item.Quantity,
			priceCents: priceCents,
		})
	}

	// Insert main order record
	var orderID int64
	var createdAt, updatedAt time.Time
	insertOrderQuery := `
		INSERT INTO orders (user_id, status, total_cents, created_at, updated_at)
		VALUES ($1, 'pending', $2, NOW(), NOW())
		RETURNING id, created_at, updated_at
	`
	err = tx.QueryRow(ctx, insertOrderQuery, userID, totalCents).Scan(&orderID, &createdAt, &updatedAt)
	if err != nil {
		log.Printf("[ORDER ERROR] Insert order record failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert order: " + err.Error()})
		return
	}

	// Insert order items
	var responseItems []OrderItemResponse
	for _, det := range details {
		var itemID int64
		insertItemQuery := `
			INSERT INTO order_items (order_id, product_id, quantity, price_cents)
			VALUES ($1, $2, $3, $4)
			RETURNING id
		`
		err = tx.QueryRow(ctx, insertItemQuery, orderID, det.productID, det.quantity, det.priceCents).Scan(&itemID)
		if err != nil {
			log.Printf("[ORDER ERROR] Insert order item failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert order item: " + err.Error()})
			return
		}

		responseItems = append(responseItems, OrderItemResponse{
			ID:         itemID,
			ProductID:  det.productID,
			Quantity:   det.quantity,
			PriceCents: det.priceCents,
		})
	}

	// Commit the entire transaction
	if err := tx.Commit(ctx); err != nil {
		log.Printf("[ORDER ERROR] Commit failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit order transaction: " + err.Error()})
		return
	}

	c.JSON(http.StatusCreated, OrderResponse{
		ID:         orderID,
		UserID:     userID,
		Status:     "pending",
		TotalCents: totalCents,
		Items:      responseItems,
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
	})
}

// ListOrders handles GET /orders
func (h *Handler) ListOrders(c *gin.Context) {
	userID, err := getUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT id, user_id, status, total_cents, created_at, updated_at
		FROM orders
		WHERE user_id = $1
		ORDER BY id DESC
	`, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var orders []OrderResponse
	for rows.Next() {
		var o OrderResponse
		if err := rows.Scan(&o.ID, &o.UserID, &o.Status, &o.TotalCents, &o.CreatedAt, &o.UpdatedAt); err == nil {
			orders = append(orders, o)
		}
	}

	if orders == nil {
		orders = []OrderResponse{}
	}
	c.JSON(http.StatusOK, orders)
}

// GetOrder handles GET /orders/:id
func (h *Handler) GetOrder(c *gin.Context) {
	userID, err := getUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var o OrderResponse
	err = h.db.QueryRow(ctx, `
		SELECT id, user_id, status, total_cents, created_at, updated_at
		FROM orders
		WHERE id = $1 AND user_id = $2
	`, orderID, userID).Scan(&o.ID, &o.UserID, &o.Status, &o.TotalCents, &o.CreatedAt, &o.UpdatedAt)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	rows, err := h.db.Query(ctx, `
		SELECT id, product_id, quantity, price_cents
		FROM order_items
		WHERE order_id = $1
	`, orderID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var item OrderItemResponse
			if err := rows.Scan(&item.ID, &item.ProductID, &item.Quantity, &item.PriceCents); err == nil {
				o.Items = append(o.Items, item)
			}
		}
	}

	c.JSON(http.StatusOK, o)
}

// UpdateOrderStatus handles PATCH /orders/:id/status
func (h *Handler) UpdateOrderStatus(c *gin.Context) {
	userID, err := getUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
		return
	}

	var req UpdateStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE orders
		SET status = $1, updated_at = NOW()
		WHERE id = $2 AND user_id = $3
	`, req.Status, orderID, userID)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found or unauthorized"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "order status updated", "status": req.Status})
}
