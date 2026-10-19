package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"bazaar/internal/database"
	"bazaar/internal/product"

	"github.com/gin-gonic/gin"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		connStr = "postgresql://neondb_owner:npg_zX7fAWDKIR4t@ep-lingering-sky-b4fp5o62-pooler.c-6.us-east-2.aws.neon.tech/neondb?sslmode=require"
	}

	dbPool, err := database.ConnectPool(context.Background(), connStr)
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

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

	slog.Info("server starting on :8080")
	if err := router.Run(":8080"); err != nil {
		slog.Error("server failed to start", "error", err)
	}
}
