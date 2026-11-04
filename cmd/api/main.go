package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"golang.org/x/crypto/bcrypt"

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
	// 1. Auto-load .env from project root
	if err := godotenv.Load(); err != nil {
		log.Println("[INFO] No .env file found or error reading .env; reading from system environment")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "bazaar-development-secret-key-super-secure"
	}

	// 2. Initialize database connection pool
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("failed to connect to neon database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("failed to ping neon database: %v", err)
	}
	log.Println("[✓] Successfully connected and verified Neon PostgreSQL database")

	r := gin.Default()

	// 3. Swagger documentation routes
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	r.GET("/swagger", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/swagger/index.html")
	})

	orderHandler := order.NewHandler(pool)

	// 4. Auth routes
	authGroup := r.Group("/auth")
	{
		authGroup.POST("/register", handleRegister(pool))
		authGroup.POST("/login", handleLogin(pool, jwtSecret))
	}

	// 5. Product routes
	productGroup := r.Group("/products")
	{
		productGroup.GET("", handleListProducts(pool))
		productGroup.GET("/:id", handleGetProduct(pool))
		productGroup.POST("", middleware.RequireAuth(jwtSecret), handleCreateProduct(pool))
		productGroup.PUT("/:id", middleware.RequireAuth(jwtSecret), handleUpdateProduct(pool))
		productGroup.DELETE("/:id", middleware.RequireAuth(jwtSecret), handleDeleteProduct(pool))
	}

	// 6. Order routes (secured by JWT)
	orderGroup := r.Group("/orders")
	orderGroup.Use(middleware.RequireAuth(jwtSecret))
	{
		orderGroup.POST("", orderHandler.CreateOrder)
		orderGroup.GET("/:id", orderHandler.GetOrder)
		orderGroup.PATCH("/:id/status", orderHandler.UpdateOrderStatus)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("[*] Bazaar API starting on port %s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("server failed to start: %v", err)
	}
}

// @Summary      Register a new user
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

		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to hash password"})
			return
		}

		var userID int64
		query := `
			INSERT INTO users (email, password_hash, full_name, created_at)
			VALUES ($1, $2, $3, NOW())
			RETURNING id;
		`
		err = pool.QueryRow(c.Request.Context(), query, req.Email, string(hashedPassword), req.FullName).Scan(&userID)
		if err != nil {
			fallbackQuery := `
				INSERT INTO users (email, password_hash)
				VALUES ($1, $2)
				RETURNING id;
			`
			if fbErr := pool.QueryRow(c.Request.Context(), fallbackQuery, req.Email, string(hashedPassword)).Scan(&userID); fbErr != nil {
				c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to register user: " + fbErr.Error()})
				return
			}
		}

		c.JSON(http.StatusCreated, gin.H{
			"message": "user registered successfully",
			"user_id": userID,
		})
	}
}

// @Summary      Log in user
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        credentials body LoginRequest true "Login credentials"
// @Success      200 {object} AuthResponse
// @Failure      401 {object} ErrorResponse
// @Failure      500 {object} ErrorResponse
// @Router       /auth/login [post]
func handleLogin(pool *pgxpool.Pool, secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req LoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: err.Error()})
			return
		}

		var userID int64
		var passwordHash string

		// Query password hash across possible column naming schemas
		query := `SELECT id, password_hash FROM users WHERE email = $1 LIMIT 1`
		err := pool.QueryRow(c.Request.Context(), query, req.Email).Scan(&userID, &passwordHash)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "invalid email or password"})
				return
			}
			fallbackQuery := `SELECT id, password FROM users WHERE email = $1 LIMIT 1`
			if fbErr := pool.QueryRow(c.Request.Context(), fallbackQuery, req.Email).Scan(&userID, &passwordHash); fbErr != nil {
				c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "invalid email or password"})
				return
			}
		}

		// Verify password using bcrypt
		if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
			c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "invalid email or password"})
			return
		}

		// Generate signed JWT token
		claims := jwt.MapClaims{
			"user_id": userID,
			"sub":     strconv.FormatInt(userID, 10),
			"email":   req.Email,
			"exp":     time.Now().Add(72 * time.Hour).Unix(),
			"iat":     time.Now().Unix(),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signedToken, err := token.SignedString([]byte(secret))
		if err != nil {
			c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to generate authentication token"})
			return
		}

		c.JSON(http.StatusOK, AuthResponse{Token: signedToken})
	}
}

// @Summary      List all products
// @Tags         products
// @Produce      json
// @Success      200 {array} ProductResponse
// @Router       /products [get]
func handleListProducts(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := pool.Query(c.Request.Context(), `
			SELECT id, name, description, price_cents, stock, created_at, updated_at 
			FROM products 
			ORDER BY id ASC
		`)
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
// @Tags         products
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        payload body ProductInput true "Product attributes"
// @Success      201 {object} map[string]interface{}
// @Router       /products [post]
func handleCreateProduct(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req ProductInput
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: err.Error()})
			return
		}

		var id int64
		query := `
			INSERT INTO products (name, description, price_cents, stock, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NOW(), NOW())
			RETURNING id;
		`
		err := pool.QueryRow(c.Request.Context(), query, req.Name, req.Description, req.PriceCents, req.Stock).Scan(&id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to create product"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"id": id, "message": "product created successfully"})
	}
}

// @Summary      Update a product
// @Tags         products
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id path int true "Product ID"
// @Param        payload body ProductInput true "Updated product payload"
// @Success      200 {object} map[string]interface{}
// @Router       /products/{id} [put]
func handleUpdateProduct(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: "invalid product id"})
			return
		}

		var req ProductInput
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: err.Error()})
			return
		}

		_, err = pool.Exec(c.Request.Context(), `
			UPDATE products 
			SET name = $1, description = $2, price_cents = $3, stock = $4, updated_at = NOW()
			WHERE id = $5
		`, req.Name, req.Description, req.PriceCents, req.Stock, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to update product"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "product updated successfully"})
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
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Error: "invalid product id"})
			return
		}

		_, err = pool.Exec(c.Request.Context(), `DELETE FROM products WHERE id = $1`, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to delete product"})
			return
		}

		c.Status(http.StatusNoContent)
	}
}
