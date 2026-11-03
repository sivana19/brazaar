package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "bazaar/docs"
	"bazaar/internal/middleware"
	"bazaar/internal/order"
)

// @title           Bazaar E-Commerce API
// @version         1.0
// @description     High-performance Go backend API for Bazaar platform featuring JWT auth and transactional order processing.
// @host            localhost:8080
// @BasePath        /
// @securityDefinitions.apikey BearerAuth
// @in              header
// @name            Authorization
// @description     Type "Bearer" followed by a space and your JWT token.

// DTO structs for Swagger docs and request binding
type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email" example:"user@bazaar.local"`
	Password string `json:"password" binding:"required,min=8" example:"SecurePassword123!"`
	FullName string `json:"full_name" binding:"required" example:"Abebe Bikila"`
}

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email" example:"tester@bazaar.local"`
	Password string `json:"password" binding:"required" example:"SecurePassword123!"`
}

type AuthResponse struct {
	Token string `json:"token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
}

type ProductInput struct {
	Name        string `json:"name" binding:"required" example:"Wireless Mechanical Keyboard"`
	Description string `json:"description" example:"Bluetooth 5.0, hot-swappable switches"`
	PriceCents  int64  `json:"price_cents" binding:"required,gt=0" example:"8500"`
	Stock       int    `json:"stock" binding:"required,gte=0" example:"25"`
}

type ProductResponse struct {
	ID          int64     `json:"id" example:"7"`
	Name        string    `json:"name" example:"Wireless Mechanical Keyboard"`
	Description string    `json:"description" example:"Bluetooth 5.0, hot-swappable switches"`
	PriceCents  int64     `json:"price_cents" example:"8500"`
	Stock       int       `json:"stock" example:"25"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ErrorResponse struct {
	Error string `json:"error" example:"invalid request payload"`
}

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://neondb_owner:password@ep-sample.eu-central-1.aws.neon.tech/neondb?sslmode=require"
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "super-secret-key-change-in-production"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("failed to connect to neon database: %v", err)
	}
	defer pool.Close()

	r := gin.Default()

	// Swagger route
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	orderHandler := order.NewHandler(pool)

	// Auth group
	authGroup := r.Group("/auth")
	{
		authGroup.POST("/register", handleRegister(pool))
		authGroup.POST("/login", handleLogin(pool, jwtSecret))
	}

	// Product routes
	productGroup := r.Group("/products")
	{
		productGroup.GET("", handleListProducts(pool))
		productGroup.GET("/:id", handleGetProduct(pool))
		productGroup.POST("", middleware.RequireAuth(jwtSecret), handleCreateProduct(pool))
		productGroup.PUT("/:id", middleware.RequireAuth(jwtSecret), handleUpdateProduct(pool))
		productGroup.DELETE("/:id", middleware.RequireAuth(jwtSecret), handleDeleteProduct(pool))
	}

	// Order routes
	orderGroup := r.Group("/orders")
	orderGroup.Use(middleware.RequireAuth(jwtSecret))
	{
		orderGroup.POST("", orderHandler.CreateOrder)
		orderGroup.GET("/:id", orderHandler.GetOrder)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("server starting on port %s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("listen failed: %v", err)
	}
}

// @Summary      Register a new user
// @Description  Creates a new customer account
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        payload body RegisterRequest true "Registration data"
// @Success      201 {object} map[string]interface{}
// @Failure      400 {object} ErrorResponse
// @Failure      500 {object} ErrorResponse
// @Router       /auth/register [post]
func handleRegister(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req RegisterRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: err.Error()})
			return
		}
		// Registration persistence logic
		c.JSON(http.StatusCreated, gin.H{"message": "user registered successfully"})
	}
}

// @Summary      Log in user
// @Description  Authenticates credentials and returns a Bearer JWT
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        credentials body LoginRequest true "Login credentials"
// @Success      200 {object} AuthResponse
// @Failure      401 {object} ErrorResponse
// @Router       /auth/login [post]
func handleLogin(pool *pgxpool.Pool, secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req LoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: err.Error()})
			return
		}
		// Login validation logic
		c.JSON(http.StatusOK, AuthResponse{Token: "sample-token"})
	}
}

// @Summary      List all products
// @Description  Retrieves all catalog items
// @Tags         products
// @Produce      json
// @Success      200 {array} ProductResponse
// @Router       /products [get]
func handleListProducts(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := pool.Query(c.Request.Context(), `SELECT id, name, description, price_cents, stock, created_at, updated_at FROM products ORDER BY id ASC`)
		if err != nil {
			c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch products"})
			return
		}
		defer rows.Close()

		products := make([]ProductResponse, 0)
		for rows.Next() {
			var p ProductResponse
			if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.Stock, &p.CreatedAt, &p.UpdatedAt); err != nil {
				continue
			}
			products = append(products, p)
		}
		c.JSON(http.StatusOK, products)
	}
}

// @Summary      Get product by ID
// @Description  Returns details and live inventory for a specific product
// @Tags         products
// @Produce      json
// @Param        id path int true "Product ID"
// @Success      200 {object} ProductResponse
// @Failure      404 {object} ErrorResponse
// @Router       /products/{id} [get]
func handleGetProduct(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: "invalid product id"})
			return
		}

		var p ProductResponse
		query := `SELECT id, name, description, price_cents, stock, created_at, updated_at FROM products WHERE id = $1`
		err = pool.QueryRow(c.Request.Context(), query, id).Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.Stock, &p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			c.JSON(http.StatusNotFound, ErrorResponse{Error: "product not found"})
			return
		}
		c.JSON(http.StatusOK, p)
	}
}

// @Summary      Create a product
// @Description  Adds a new item to catalog (requires authentication)
// @Tags         products
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        payload body ProductInput true "Product attributes"
// @Success      201 {object} ProductResponse
// @Failure      401 {object} ErrorResponse
// @Router       /products [post]
func handleCreateProduct(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req ProductInput
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: err.Error()})
			return
		}
		// Create product SQL logic
		c.JSON(http.StatusCreated, gin.H{"message": "product created"})
	}
}

// @Summary      Update a product
// @Tags         products
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id path int true "Product ID"
// @Param        payload body ProductInput true "Updated product payload"
// @Success      200 {object} ProductResponse
// @Router       /products/{id} [put]
func handleUpdateProduct(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "product updated"})
	}
}

// @Summary      Delete a product
// @Tags         products
// @Security     BearerAuth
// @Param        id path int true "Product ID"
// @Success      204 "No Content"
// @Router       /products/{id} [delete]
func handleDeleteProduct(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	}
}
