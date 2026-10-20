package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"bazaar/internal/database"
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

	slog.Info(fmt.Sprintf("server starting on :%s", port))
	if err := router.Run(":" + port); err != nil {
		slog.Error("server failed to start", "error", err)
	}
}
