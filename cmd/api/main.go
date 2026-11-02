package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "bazaar/docs"
	"bazaar/internal/database"
	"bazaar/internal/middleware"
	"bazaar/internal/order"
	"bazaar/internal/product"
	"bazaar/internal/user"
)

// @title           Bazaar E-Commerce API
// @version         1.0
// @description     REST API for Bazaar e-commerce platform with Neon PostgreSQL, concurrency locks, and JWT auth.
// @host            localhost:8080
// @BasePath        /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Type "Bearer" followed by a space and your JWT token.
func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Note: .env file not found or already in environment")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL must be set in environment")
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "bazaar_super_secret_jwt_key_2026"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Database Connection & Migrations
	dbPool, err := database.ConnectPool(ctx, dbURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer dbPool.Close()

	if err := database.AutoMigrate(ctx, dbPool); err != nil {
		log.Fatalf("failed to run migrations: %v", err)
	}

	// 2. Initialize Repositories and Handlers
	userRepo := user.NewRepository(dbPool, jwtSecret)
	productRepo := product.NewRepository(dbPool)
	orderHandler := order.NewHandler(dbPool)

	router := gin.Default()

	// 3. Swagger Route
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// 4. Public Authentication Routes
	router.POST("/auth/register", func(c *gin.Context) {
		var input user.RegisterInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		u, err := userRepo.Register(c.Request.Context(), input)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"message": "user registered", "user": u})
	})

	router.POST("/auth/login", func(c *gin.Context) {
		var input user.LoginInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		token, u, err := userRepo.Login(c.Request.Context(), input)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": token, "user": u})
	})

	// 5. Public Product Catalog Routes
	router.GET("/products", func(c *gin.Context) {
		var filter product.ListProductsFilter
		if err := c.ShouldBindQuery(&filter); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		resp, err := productRepo.List(c.Request.Context(), filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, resp)
	})

	router.GET("/products/:id", func(c *gin.Context) {
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
		c.JSON(http.StatusOK, p)
	})

	// 6. Protected Routes (Require JWT Auth)
	protected := router.Group("/")
	protected.Use(middleware.RequireAuth(jwtSecret))
	{
		// Product management
		protected.POST("/products", func(c *gin.Context) {
			var input product.CreateProductInput
			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			p, err := productRepo.Create(c.Request.Context(), input)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusCreated, p)
		})

		protected.PUT("/products/:id", func(c *gin.Context) {
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
			c.JSON(http.StatusOK, p)
		})

		protected.DELETE("/products/:id", func(c *gin.Context) {
			id, err := strconv.Atoi(c.Param("id"))
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
				return
			}
			success, err := productRepo.Delete(c.Request.Context(), id)
			if err != nil || !success {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete product"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"message": "product deleted"})
		})

		// Orders
		protected.POST("/orders", orderHandler.CreateOrder)
		protected.GET("/orders/:id", orderHandler.GetOrder)
	}

	// 7. Server Setup with Graceful Shutdown
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: router,
	}

	go func() {
		log.Printf("server starting on port %s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen failed: %s\n", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down server gracefully...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal("server forced to shutdown:", err)
	}

	log.Println("server exited cleanly")
}
