package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"bazaar/internal/database"
	"bazaar/internal/order"
	"bazaar/internal/product"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	// Configure JSON structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Load environment variables from .env file
	if err := godotenv.Load(); err != nil {
		slog.Warn("No .env file found or failed to load, falling back to system environment variables")
	}

	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		slog.Error("DATABASE_URL environment variable is required")
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Establish pooled connection
	dbPool, err := database.ConnectPool(context.Background(), connStr)
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	// Initialize repositories
	productRepo := product.NewRepository(dbPool)
	orderRepo := order.NewRepository(dbPool)

	router := gin.Default()

	// GET /health - Service and database health check
	router.GET("/health", func(c *gin.Context) {
		if err := dbPool.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "database unreachable",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":   "healthy",
			"database": "connected",
		})
	})

	// GET /products - List all products
	router.GET("/products", func(c *gin.Context) {
		products, err := productRepo.GetAll(c.Request.Context())
		if err != nil {
			slog.Error("failed to fetch products", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to fetch products",
			})
			return
		}
		c.JSON(http.StatusOK, products)
	})

	// GET /products/:id - Retrieve a single product by ID
	router.GET("/products/:id", func(c *gin.Context) {
		idParam := c.Param("id")
		id, err := strconv.Atoi(idParam)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "invalid product id, must be a positive integer",
			})
			return
		}

		p, err := productRepo.GetByID(c.Request.Context(), id)
		if err != nil {
			slog.Error("failed to fetch product by id", "id", id, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to fetch product",
			})
			return
		}

		if p == nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "product not found",
			})
			return
		}

		c.JSON(http.StatusOK, p)
	})

	// POST /products - Create a new product item
	router.POST("/products", func(c *gin.Context) {
		var input product.CreateProductInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "invalid request payload",
				"details": err.Error(),
			})
			return
		}

		newProduct, err := productRepo.Create(c.Request.Context(), input)
		if err != nil {
			slog.Error("failed to create product", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to create product",
			})
			return
		}

		c.JSON(http.StatusCreated, newProduct)
	})

	// PUT /products/:id - Update an existing product
	router.PUT("/products/:id", func(c *gin.Context) {
		idParam := c.Param("id")
		id, err := strconv.Atoi(idParam)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "invalid product id, must be a positive integer",
			})
			return
		}

		var input product.UpdateProductInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "invalid request payload",
				"details": err.Error(),
			})
			return
		}

		updatedProduct, err := productRepo.Update(c.Request.Context(), id, input)
		if err != nil {
			slog.Error("failed to update product", "id", id, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to update product",
			})
			return
		}

		if updatedProduct == nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "product not found",
			})
			return
		}

		c.JSON(http.StatusOK, updatedProduct)
	})

	// DELETE /products/:id - Delete a product
	router.DELETE("/products/:id", func(c *gin.Context) {
		idParam := c.Param("id")
		id, err := strconv.Atoi(idParam)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "invalid product id, must be a positive integer",
			})
			return
		}

		deleted, err := productRepo.Delete(c.Request.Context(), id)
		if err != nil {
			slog.Error("failed to delete product", "id", id, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to delete product",
			})
			return
		}

		if !deleted {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "product not found",
			})
			return
		}

		c.Status(http.StatusNoContent)
	})

	// POST /orders - Atomic checkout and inventory deduction
	router.POST("/orders", func(c *gin.Context) {
		var input order.CreateOrderInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "invalid order request",
				"details": err.Error(),
			})
			return
		}

		newOrder, err := orderRepo.CreateOrder(c.Request.Context(), input)
		if err != nil {
			switch {
			case errors.Is(err, order.ErrInsufficientStock):
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			case errors.Is(err, order.ErrProductNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			case errors.Is(err, order.ErrEmptyOrder):
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			default:
				slog.Error("order creation failed", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to complete order"})
			}
			return
		}

		c.JSON(http.StatusCreated, newOrder)
	})

	// GET /orders/:id - Lookup order by ID with line items
	router.GET("/orders/:id", func(c *gin.Context) {
		idParam := c.Param("id")
		id, err := strconv.Atoi(idParam)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "invalid order id, must be a positive integer",
			})
			return
		}

		o, err := orderRepo.GetByID(c.Request.Context(), id)
		if err != nil {
			slog.Error("failed to fetch order", "id", id, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to fetch order",
			})
			return
		}

		if o == nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "order not found",
			})
			return
		}

		c.JSON(http.StatusOK, o)
	})

	// Configure server with graceful shutdown
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: router,
	}

	go func() {
		slog.Info(fmt.Sprintf("server starting on :%s", port))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server startup failed", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for termination signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
	}

	slog.Info("server exited cleanly")
}
