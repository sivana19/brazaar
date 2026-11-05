package order

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	pool *pgxpool.Pool
}

func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

func getUserID(c *gin.Context) (int64, error) {
	val, exists := c.Get("user_id")
	if !exists {
		return 0, errors.New("user_id missing from authentication context")
	}

	switch v := val.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("unsupported user_id type: %T", val)
	}
}

// CreateOrder godoc
// @Summary      Create a new order
// @Description  Places an order atomically, validates inventory, and deducts stock
// @Tags         orders
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        payload body CreateOrderRequest true "Order details"
// @Success      201 {object} OrderResponse
// @Failure      400 {object} map[string]interface{}
// @Failure      401 {object} map[string]interface{}
// @Failure      500 {object} map[string]interface{}
// @Router       /orders [post]
func (h *Handler) CreateOrder(c *gin.Context) {
	userID, err := getUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: " + err.Error()})
		return
	}

	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload: " + err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to begin transaction: " + err.Error()})
		return
	}
	defer tx.Rollback(ctx)

	var totalCents int64 = 0
	type orderItemPrep struct {
		productID  int64
		quantity   int
		priceCents int64
	}
	itemsToInsert := make([]orderItemPrep, 0, len(req.Items))

	for _, item := range req.Items {
		var stock int
		var priceCents int64

		query := `SELECT stock, price_cents FROM products WHERE id = $1 FOR UPDATE`
		err := tx.QueryRow(ctx, query, item.ProductID).Scan(&stock, &priceCents)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("product %d not found", item.ProductID)})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error verifying product: " + err.Error()})
			return
		}

		if stock < item.Quantity {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("insufficient stock for product %d: available %d, requested %d", item.ProductID, stock, item.Quantity)})
			return
		}

		_, err = tx.Exec(ctx, `UPDATE products SET stock = stock - $1, updated_at = NOW() WHERE id = $2`, item.Quantity, item.ProductID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update stock: " + err.Error()})
			return
		}

		totalCents += priceCents * int64(item.Quantity)
		itemsToInsert = append(itemsToInsert, orderItemPrep{
			productID:  item.ProductID,
			quantity:   item.Quantity,
			priceCents: priceCents,
		})
	}

	var orderID int64
	var createdAt, updatedAt time.Time
	orderQuery := `
		INSERT INTO orders (user_id, status, total_cents, created_at, updated_at)
		VALUES ($1, 'pending', $2, NOW(), NOW())
		RETURNING id, created_at, updated_at;
	`
	err = tx.QueryRow(ctx, orderQuery, userID, totalCents).Scan(&orderID, &createdAt, &updatedAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert order: " + err.Error()})
		return
	}

	responseItems := make([]OrderItemResponse, 0, len(itemsToInsert))
	for _, it := range itemsToInsert {
		var itemID int64
		itemQuery := `
			INSERT INTO order_items (order_id, product_id, quantity, price_cents)
			VALUES ($1, $2, $3, $4)
			RETURNING id;
		`
		err = tx.QueryRow(ctx, itemQuery, orderID, it.productID, it.quantity, it.priceCents).Scan(&itemID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert order item: " + err.Error()})
			return
		}

		responseItems = append(responseItems, OrderItemResponse{
			ID:         itemID,
			ProductID:  it.productID,
			Quantity:   it.quantity,
			PriceCents: it.priceCents,
		})
	}

	if err := tx.Commit(ctx); err != nil {
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

// ListOrders godoc
// @Summary      List user orders
// @Description  Fetches paginated order history for authenticated user
// @Tags         orders
// @Security     BearerAuth
// @Produce      json
// @Param        page  query int false "Page number" default(1)
// @Param        limit query int false "Items per page" default(10)
// @Success      200 {object} map[string]interface{}
// @Failure      401 {object} map[string]interface{}
// @Failure      500 {object} map[string]interface{}
// @Router       /orders [get]
func (h *Handler) ListOrders(c *gin.Context) {
	userID, err := getUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}
	offset := (page - 1) * limit

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	var total int
	err = h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM orders WHERE user_id = $1`, userID).Scan(&total)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count orders: " + err.Error()})
		return
	}

	rows, err := h.pool.Query(ctx, `
		SELECT id, user_id, status, total_cents, created_at, updated_at
		FROM orders
		WHERE user_id = $1
		ORDER BY id DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch orders: " + err.Error()})
		return
	}
	defer rows.Close()

	orders := make([]OrderResponse, 0)
	for rows.Next() {
		var o OrderResponse
		if err := rows.Scan(&o.ID, &o.UserID, &o.Status, &o.TotalCents, &o.CreatedAt, &o.UpdatedAt); err == nil {
			orders = append(orders, o)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"total": total,
		"page":  page,
		"limit": limit,
		"data":  orders,
	})
}

// GetOrder godoc
// @Summary      Get order by ID
// @Description  Fetches order details and line items
// @Tags         orders
// @Security     BearerAuth
// @Produce      json
// @Param        id path int true "Order ID"
// @Success      200 {object} OrderResponse
// @Failure      400 {object} map[string]interface{}
// @Failure      404 {object} map[string]interface{}
// @Router       /orders/{id} [get]
func (h *Handler) GetOrder(c *gin.Context) {
	userID, err := getUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	var o OrderResponse
	err = h.pool.QueryRow(ctx, `
		SELECT id, user_id, status, total_cents, created_at, updated_at
		FROM orders
		WHERE id = $1 AND user_id = $2
	`, orderID, userID).Scan(&o.ID, &o.UserID, &o.Status, &o.TotalCents, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	rows, err := h.pool.Query(ctx, `
		SELECT id, product_id, quantity, price_cents
		FROM order_items
		WHERE order_id = $1
	`, orderID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var it OrderItemResponse
			if err := rows.Scan(&it.ID, &it.ProductID, &it.Quantity, &it.PriceCents); err == nil {
				o.Items = append(o.Items, it)
			}
		}
	}

	c.JSON(http.StatusOK, o)
}

// UpdateOrderStatus godoc
// @Summary      Update order status
// @Description  Updates order status and restores stock if cancelled
// @Tags         orders
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id path int true "Order ID"
// @Param        payload body UpdateStatusRequest true "Status update payload"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} map[string]interface{}
// @Failure      404 {object} map[string]interface{}
// @Failure      500 {object} map[string]interface{}
// @Router       /orders/{id}/status [patch]
func (h *Handler) UpdateOrderStatus(c *gin.Context) {
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

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to begin transaction"})
		return
	}
	defer tx.Rollback(ctx)

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&currentStatus)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	// Restock products if order transitions to cancelled
	if req.Status == "cancelled" && currentStatus != "cancelled" {
		rows, err := tx.Query(ctx, `SELECT product_id, quantity FROM order_items WHERE order_id = $1`, orderID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve order items"})
			return
		}
		defer rows.Close()

		type itemRestore struct {
			productID int64
			qty       int
		}
		var restoreList []itemRestore
		for rows.Next() {
			var r itemRestore
			if err := rows.Scan(&r.productID, &r.qty); err == nil {
				restoreList = append(restoreList, r)
			}
		}
		rows.Close()

		for _, item := range restoreList {
			_, err = tx.Exec(ctx, `UPDATE products SET stock = stock + $1, updated_at = NOW() WHERE id = $2`, item.qty, item.productID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to restock product"})
				return
			}
		}
	}

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
		"message": "order status updated successfully",
		"status":  req.Status,
	})
}
