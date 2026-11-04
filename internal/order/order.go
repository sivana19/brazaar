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

// Handler manages order-related HTTP requests and database interactions.
type Handler struct {
	pool *pgxpool.Pool
}

// NewHandler constructs a new Handler instance.
func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

// OrderItemRequest represents an individual line item in the order submission.
type OrderItemRequest struct {
	ProductID int64 `json:"product_id" binding:"required,gt=0" example:"7"`
	Quantity  int   `json:"quantity" binding:"required,gt=0" example:"2"`
}

// CreateOrderRequest represents the payload required to submit a new order.
type CreateOrderRequest struct {
	Items []OrderItemRequest `json:"items" binding:"required,min=1"`
}

// UpdateStatusRequest represents the payload for transitioning order lifecycle states.
type UpdateStatusRequest struct {
	Status string `json:"status" binding:"required" example:"paid"`
}

// OrderItemResponse formats an order line item in responses.
type OrderItemResponse struct {
	ID         int64  `json:"id" example:"101"`
	ProductID  int64  `json:"product_id" example:"7"`
	Quantity   int    `json:"quantity" example:"2"`
	PriceCents int64  `json:"price_cents" example:"11999"`
	Name       string `json:"name,omitempty" example:"Ergonomic Mechanical Keyboard"`
}

// OrderResponse formats complete order details.
type OrderResponse struct {
	ID         int64               `json:"id" example:"1"`
	UserID     int64               `json:"user_id" example:"42"`
	Status     string              `json:"status" example:"pending"`
	TotalCents int64               `json:"total_cents" example:"23998"`
	Items      []OrderItemResponse `json:"items,omitempty"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

// Allowed state transitions for orders.
var allowedTransitions = map[string][]string{
	"pending":   {"paid", "cancelled"},
	"paid":      {"shipped", "cancelled"},
	"shipped":   {"delivered"},
	"delivered": {},
	"cancelled": {},
}

func isValidTransition(current, next string) bool {
	validNextList, exists := allowedTransitions[current]
	if !exists {
		return false
	}
	for _, target := range validNextList {
		if target == next {
			return true
		}
	}
	return false
}

// getUserIDFromContext defensively extracts user ID across numeric or string claims.
func getUserIDFromContext(c *gin.Context) (int64, error) {
	val, exists := c.Get("user_id")
	if !exists {
		val, exists = c.Get("id")
	}
	if !exists {
		val, exists = c.Get("sub")
	}
	if !exists {
		return 0, errors.New("unauthorized: missing user identity in context")
	}

	switch v := val.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, errors.New("unauthorized: invalid user id claim format")
		}
		return parsed, nil
	default:
		return 0, errors.New("unauthorized: unsupported user id claim type")
	}
}

// CreateOrder handles transactional creation of orders with pessimistic inventory locking.
// @Summary      Create a new order
// @Description  Places an order for items, deducting inventory via row-level locks
// @Tags         orders
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        payload body CreateOrderRequest true "Order details"
// @Success      201 {object} OrderResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      409 {object} map[string]string "Insufficient inventory"
// @Failure      500 {object} map[string]string
// @Router       /orders [post]
func (h *Handler) CreateOrder(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
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
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to begin transaction"})
		return
	}
	defer tx.Rollback(ctx)

	var totalCents int64
	type evaluatedItem struct {
		productID  int64
		quantity   int
		priceCents int64
	}
	itemsToInsert := make([]evaluatedItem, 0, len(req.Items))

	for _, item := range req.Items {
		var currentStock int
		var priceCents int64

		// Lock product row to prevent race conditions
		query := `SELECT stock, price_cents FROM products WHERE id = $1 FOR UPDATE`
		err := tx.QueryRow(ctx, query, item.ProductID).Scan(&currentStock, &priceCents)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("product with id %d not found", item.ProductID),
				})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database query error"})
			return
		}

		if currentStock < item.Quantity {
			c.JSON(http.StatusConflict, gin.H{
				"error": fmt.Sprintf("insufficient stock for product %d: requested %d, available %d",
					item.ProductID, item.Quantity, currentStock),
			})
			return
		}

		// Decrement inventory
		_, err = tx.Exec(ctx, `UPDATE products SET stock = stock - $1, updated_at = NOW() WHERE id = $2`, item.Quantity, item.ProductID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to deduct inventory"})
			return
		}

		lineTotal := priceCents * int64(item.Quantity)
		totalCents += lineTotal

		itemsToInsert = append(itemsToInsert, evaluatedItem{
			productID:  item.ProductID,
			quantity:   item.Quantity,
			priceCents: priceCents,
		})
	}

	// Insert main order record
	var orderID int64
	var createdAt, updatedAt time.Time
	orderQuery := `
		INSERT INTO orders (user_id, status, total_cents, created_at, updated_at)
		VALUES ($1, 'pending', $2, NOW(), NOW())
		RETURNING id, created_at, updated_at;
	`
	err = tx.QueryRow(ctx, orderQuery, userID, totalCents).Scan(&orderID, &createdAt, &updatedAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record order"})
		return
	}

	// Insert order items
	responseItems := make([]OrderItemResponse, 0, len(itemsToInsert))
	for _, item := range itemsToInsert {
		var itemID int64
		itemInsert := `
			INSERT INTO order_items (order_id, product_id, quantity, price_cents)
			VALUES ($1, $2, $3, $4)
			RETURNING id;
		`
		err = tx.QueryRow(ctx, itemInsert, orderID, item.productID, item.quantity, item.priceCents).Scan(&itemID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record order items"})
			return
		}

		responseItems = append(responseItems, OrderItemResponse{
			ID:         itemID,
			ProductID:  item.productID,
			Quantity:   item.quantity,
			PriceCents: item.priceCents,
		})
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
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

// GetOrder retrieves an order by its ID.
// @Summary      Get order by ID
// @Description  Returns single order details and its line items
// @Tags         orders
// @Security     BearerAuth
// @Produce      json
// @Param        id path int true "Order ID"
// @Success      200 {object} OrderResponse
// @Failure      400 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /orders/{id} [get]
func (h *Handler) GetOrder(c *gin.Context) {
	orderIDStr := c.Param("id")
	orderID, err := strconv.ParseInt(orderIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
		return
	}

	ctx := c.Request.Context()
	var resp OrderResponse

	query := `SELECT id, user_id, status, total_cents, created_at, updated_at FROM orders WHERE id = $1`
	err = h.pool.QueryRow(ctx, query, orderID).Scan(
		&resp.ID,
		&resp.UserID,
		&resp.Status,
		&resp.TotalCents,
		&resp.CreatedAt,
		&resp.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database query error"})
		return
	}

	// Fetch line items
	itemRows, err := h.pool.Query(ctx, `
		SELECT oi.id, oi.product_id, oi.quantity, oi.price_cents, COALESCE(p.name, '')
		FROM order_items oi
		LEFT JOIN products p ON oi.product_id = p.id
		WHERE oi.order_id = $1
		ORDER BY oi.id ASC
	`, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch order line items"})
		return
	}
	defer itemRows.Close()

	for itemRows.Next() {
		var item OrderItemResponse
		if err := itemRows.Scan(&item.ID, &item.ProductID, &item.Quantity, &item.PriceCents, &item.Name); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to scan line items"})
			return
		}
		resp.Items = append(resp.Items, item)
	}

	c.JSON(http.StatusOK, resp)
}

// UpdateOrderStatus updates order state and restores inventory upon cancellation.
// @Summary      Update order status
// @Description  Transition order status (pending -> paid -> shipped -> delivered, or cancelled with inventory restocking)
// @Tags         orders
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id path int true "Order ID"
// @Param        payload body UpdateStatusRequest true "Target status"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} map[string]string "Invalid transition"
// @Failure      404 {object} map[string]string "Order not found"
// @Failure      500 {object} map[string]string "Database error"
// @Router       /orders/{id}/status [patch]
func (h *Handler) UpdateOrderStatus(c *gin.Context) {
	orderIDStr := c.Param("id")
	orderID, err := strconv.ParseInt(orderIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
		return
	}

	var req UpdateStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid status is required"})
		return
	}

	ctx := c.Request.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to begin transaction"})
		return
	}
	defer tx.Rollback(ctx)

	// Lock the order row to ensure consistent transition evaluation
	var currentStatus string
	queryCurrent := `SELECT status FROM orders WHERE id = $1 FOR UPDATE`
	err = tx.QueryRow(ctx, queryCurrent, orderID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve order"})
		return
	}

	if !isValidTransition(currentStatus, req.Status) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("invalid transition from '%s' to '%s'", currentStatus, req.Status),
		})
		return
	}

	// Restock products if order was cancelled
	if req.Status == "cancelled" {
		itemRows, err := tx.Query(ctx, `SELECT product_id, quantity FROM order_items WHERE order_id = $1`, orderID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read order line items"})
			return
		}

		type restockEntry struct {
			productID int64
			qty       int
		}
		var restockList []restockEntry

		for itemRows.Next() {
			var r restockEntry
			if err := itemRows.Scan(&r.productID, &r.qty); err != nil {
				itemRows.Close()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse restock item"})
				return
			}
			restockList = append(restockList, r)
		}
		itemRows.Close()

		for _, item := range restockList {
			_, err = tx.Exec(ctx, `
				UPDATE products 
				SET stock = stock + $1, updated_at = NOW() 
				WHERE id = $2
			`, item.qty, item.productID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to restore product stock"})
				return
			}
		}
	}

	// Update order status
	_, err = tx.Exec(ctx, `UPDATE orders SET status = $1, updated_at = NOW() WHERE id = $2`, req.Status, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order status"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"order_id":        orderID,
		"previous_status": currentStatus,
		"status":          req.Status,
		"message":         "order status updated successfully",
	})
}
