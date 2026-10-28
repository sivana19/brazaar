package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "bazaar/docs"
	"bazaar/internal/database"
	"bazaar/internal/middleware"
	"bazaar/internal/order"
	"bazaar/internal/product"
	"bazaar/internal/user"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// @title           Bazaar E-Commerce REST API
// @version         1.0
// @description     Production-grade e-commerce backend built with Go, Gin, and Neon PostgreSQL.
// @host            localhost:8000
// @BasePath        /

// @securityDefinitions.apikey BearerAuth
// @in              header
// @name            Authorization
// @description     Type "Bearer " followed by your JWT token.
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	_ = godotenv.Load()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "super_secret_bazaar_token_key_2026"
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL environment variable is not set")
		os.Exit(1)
	}

	initCtx, initCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer initCancel()

	dbPool, err := database.ConnectPool(initCtx, dbURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	userRepo := user.NewRepository(dbPool, jwtSecret)
	productRepo := product.NewRepository(dbPool)
	orderRepo := order.NewRepository(dbPool)

	r := gin.Default()

	// Swagger UI documentation endpoint
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// @Summary      Health Check
	// @Description  Verifies the API and underlying database connectivity
	// @Tags         System
	// @Produce      json
	// @Success      200 {object} map[string]string
	// @Failure      503 {object} map[string]string
	// @Router       /health [get]
	r.GET("/health", func(c *gin.Context) {
		if err := dbPool.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "db": "down"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "healthy", "db": "up"})
	})

	// Public Auth endpoints
	authGroup := r.Group("/auth")
	{
		// @Summary      Register User
		// @Description  Register a new account
		// @Tags         Auth
		// @Accept       json
		// @Produce      json
		// @Param        payload body user.RegisterInput true "Registration Payload"
		// @Success      201 {object} map[string]user.User
		// @Failure      400 {object} map[string]string
		// @Failure      500 {object} map[string]string
		// @Router       /auth/register [post]
		authGroup.POST("/register", func(c *gin.Context) {
			var input user.RegisterInput
			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			u, err := userRepo.Register(c.Request.Context(), input)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusCreated, gin.H{"user": u})
		})

		// @Summary      Login User
		// @Description  Authenticate credentials and return a signed JWT token
		// @Tags         Auth
		// @Accept       json
		// @Produce      json
		// @Param        payload body user.LoginInput true "Login Credentials"
		// @Success      200 {object} map[string]any
		// @Failure      400 {object} map[string]string
		// @Failure      401 {object} map[string]string
		// @Failure      500 {object} map[string]string
		// @Router       /auth/login [post]
		authGroup.POST("/login", func(c *gin.Context) {
			var input user.LoginInput
			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			token, u, err := userRepo.Login(c.Request.Context(), input)
			if err != nil {
				if errors.Is(err, user.ErrInvalidCredentials) {
					c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"token": token, "user": u})
		})
	}

	// @Summary      List Products
	// @Description  Search, filter by price range, and paginate product catalog
	// @Tags         Products
	// @Produce      json
	// @Param        search query string false "Search query keyword"
	// @Param        min_price query int false "Minimum price in cents"
	// @Param        max_price query int false "Maximum price in cents"
	// @Param        limit query int false "Items per page (default 20, max 100)"
	// @Param        offset query int false "Pagination offset"
	// @Success      200 {object} product.PaginatedProductsResponse
	// @Failure      400 {object} map[string]string
	// @Failure      500 {object} map[string]string
	// @Router       /products [get]
	r.GET("/products", func(c *gin.Context) {
		var filter product.ListProductsFilter
		if err := c.ShouldBindQuery(&filter); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query parameters"})
			return
		}

		result, err := productRepo.List(c.Request.Context(), filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	})

	// @Summary      Get Product By ID
	// @Description  Fetch a single product record by its primary key
	// @Tags         Products
	// @Produce      json
	// @Param        id path int true "Product ID"
	// @Success      200 {object} map[string]product.Product
	// @Failure      400 {object} map[string]string
	// @Failure      404 {object} map[string]string
	// @Router       /products/{id} [get]
	r.GET("/products/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
			return
		}
		p, err := productRepo.GetByID(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"product": p})
	})

	// Protected Product modifications (Admin Only)
	adminProducts := r.Group("/products")
	adminProducts.Use(middleware.RequireAuth(jwtSecret), middleware.RequireRole("admin"))
	{
		// @Summary      Create Product
		// @Description  Add a new product to the catalog (Admin only)
		// @Tags         Products
		// @Accept       json
		// @Produce      json
		// @Security     BearerAuth
		// @Param        payload body product.CreateProductInput true "Product Payload"
		// @Success      201 {object} map[string]product.Product
		// @Failure      400 {object} map[string]string
		// @Failure      401 {object} map[string]string
		// @Failure      403 {object} map[string]string
		// @Failure      500 {object} map[string]string
		// @Router       /products [post]
		adminProducts.POST("", func(c *gin.Context) {
			var input product.CreateProductInput
			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}

			if err := input.Validate(); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}

			p, err := productRepo.Create(c.Request.Context(), input)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusCreated, gin.H{"product": p})
		})

		// @Summary      Update Product
		// @Description  Update details of an existing product (Admin only)
		// @Tags         Products
		// @Accept       json
		// @Produce      json
		// @Security     BearerAuth
		// @Param        id path int true "Product ID"
		// @Param        payload body product.UpdateProductInput true "Update Fields"
		// @Success      200 {object} map[string]product.Product
		// @Failure      400 {object} map[string]string
		// @Failure      401 {object} map[string]string
		// @Failure      403 {object} map[string]string
		// @Failure      500 {object} map[string]string
		// @Router       /products/{id} [put]
		adminProducts.PUT("/:id", func(c *gin.Context) {
			id, err := strconv.Atoi(c.Param("id"))
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
				return
			}
			var input product.UpdateProductInput
			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			p, err := productRepo.Update(c.Request.Context(), id, input)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"product": p})
		})

		// @Summary      Delete Product
		// @Description  Remove a product from the database (Admin only)
		// @Tags         Products
		// @Security     BearerAuth
		// @Param        id path int true "Product ID"
		// @Success      204 "No Content"
		// @Failure      400 {object} map[string]string
		// @Failure      401 {object} map[string]string
		// @Failure      403 {object} map[string]string
		// @Failure      500 {object} map[string]string
		// @Router       /products/{id} [delete]
		adminProducts.DELETE("/:id", func(c *gin.Context) {
			id, err := strconv.Atoi(c.Param("id"))
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
				return
			}
			_, err = productRepo.Delete(c.Request.Context(), id)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.Status(http.StatusNoContent)
		})
	}

	// Order endpoints
	// @Summary      Create Order
	// @Description  Place an order and execute transactional inventory deduction
	// @Tags         Orders
	// @Accept       json
	// @Produce      json
	// @Param        payload body order.CreateOrderInput true "Order Items Payload"
	// @Success      201 {object} map[string]order.Order
	// @Failure      400 {object} map[string]string
	// @Router       /orders [post]
	r.POST("/orders", func(c *gin.Context) {
		var input order.CreateOrderInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ord, err := orderRepo.CreateOrder(c.Request.Context(), input)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"order": ord})
	})

	// @Summary      Get Order By ID
	// @Description  Retrieve order receipt details with item breakdowns
	// @Tags         Orders
	// @Produce      json
	// @Param        id path int true "Order ID"
	// @Success      200 {object} map[string]order.Order
	// @Failure      400 {object} map[string]string
	// @Failure      404 {object} map[string]string
	// @Router       /orders/{id} [get]
	r.GET("/orders/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
			return
		}
		ord, err := orderRepo.GetByID(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"order": ord})
	})

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	go func() {
		slog.Info("server starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed to start", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
	}

	slog.Info("server exited cleanly")
}
